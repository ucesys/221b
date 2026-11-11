package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/nodessh"
	"github.com/ucesys/221b/server/internal/toolbox"
)

// SSH to nodes (Phase 11, ADR-72) and the hosts outside Kubernetes
// (Phase 12).

// wireSSH reads the toolbox's configuration and opens SSH to nodes, with
// the hosts outside Kubernetes.
func (w *wiring) wireSSH(ctx context.Context) error {
	cfg, log := w.cfg, w.log
	// The toolbox (Phase 9): sessions open from approved Watson cards and
	// live on the leader, like the assistants (TB-14).
	toolboxCfg, err := toolbox.Read(cfg.ToolboxConfigFile)
	if err != nil {
		return err
	}
	if w.authCfg.Mode == auth.ModeNone {
		toolboxCfg.SSH.Enabled = false
	}
	// SSH to nodes (Phase 11, ADR-72): the console's certificate
	// authority lives in a Secret beside the connections, and the pairings
	// beside the settings.
	var caStore *kube.SecretKey
	var console string
	if cfg.ConnectionsSecrets != "" {
		ns, prefix, _ := strings.Cut(cfg.ConnectionsSecrets, "/")
		sk, err := kube.NewSecretKey(w.restCfg, ns, prefix+"-ssh-ca", map[string]string{"app.kubernetes.io/name": "221b", "app.kubernetes.io/component": "ssh-ca", "app.kubernetes.io/managed-by": "221b"})
		if err != nil {
			return fmt.Errorf("the SSH authority's Secret: %w", err)
		}
		caStore, console = sk, nodessh.ConsoleName(ns, prefix)
	} else {
		log.Warn("the SSH authority's key is kept in memory and made again on restart; paired nodes then need pairing again (set -connections-secrets to keep it in a Secret)")
	}
	// Hosts outside Kubernetes (Phase 12) are kept beside the pairings and
	// shown on their cluster's Nodes tab as they are read or written.
	hosts := nodessh.NewHosts(w.spendBackend)
	hosts.OnRead(func(all map[string]nodessh.Host) {
		by := nodessh.ByCluster(all)
		for _, c := range w.manager.List() {
			if st, _, ok := w.manager.Lookup(c.ID); ok && st != nil {
				st.SetHosts(nodessh.HostRows(by[c.ID]))
			}
		}
	})
	go func() {
		// Another replica's writes, and the first read: the leader shows
		// them within a refresh.
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			lctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			if _, err := hosts.Load(lctx); err != nil {
				log.Warn("the hosts could not be read", "err", err)
			}
			cancel()
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	sshSvc := nodessh.New(nodessh.Options{
		CA:       nodessh.NewCA(caStore),
		Pairings: nodessh.NewPairings(w.spendBackend),
		Hosts:    hosts,
		// A host that takes its hostname keeps its place in lists of nodes.
		OnRename: func(ctx context.Context, cluster, from, to string) {
			if _, err := w.shared.RenameNode(ctx, cluster, from, to); err != nil {
				log.Warn("a renamed host's lists of nodes could not follow", "cluster", cluster, "from", from, "to", to, "err", err)
			}
		},
		IsNode: func(cluster, name string) bool {
			_, client, ok := w.manager.Lookup(cluster)
			if !ok || client == nil {
				return false
			}
			nodes, ok := client.(interface{ Node(string) *corev1.Node })
			return ok && nodes.Node(name) != nil
		},
		Mask:    func(w io.Writer, secrets []string) io.Writer { return automation.NewMask(w, secrets) },
		Console: console,
		Logger:  log.With("component", "ssh"),
		NodeAddr: func(cluster, node string) (string, error) {
			_, client, ok := w.manager.Lookup(cluster)
			if !ok || client == nil {
				return "", fmt.Errorf("no connection %q", cluster)
			}
			nodes, ok := client.(interface{ Node(string) *corev1.Node })
			if !ok {
				return "", errors.New("this connection does not list nodes")
			}
			n := nodes.Node(node)
			if n == nil {
				return "", fmt.Errorf("node %s is not known here", node)
			}
			for _, a := range n.Status.Addresses {
				if a.Type == corev1.NodeInternalIP {
					return a.Address, nil
				}
			}
			for _, a := range n.Status.Addresses {
				if a.Type == corev1.NodeExternalIP || a.Type == corev1.NodeInternalDNS || a.Type == corev1.NodeHostName {
					return a.Address, nil
				}
			}
			return "", fmt.Errorf("node %s has no address", node)
		},
	})
	w.toolboxCfg, w.hosts, w.sshSvc = toolboxCfg, hosts, sshSvc
	return nil
}
