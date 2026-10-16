// Command 221b-sim runs a simulated Kubernetes API server with as many nodes
// and pods as you like, for developing and testing 221B without a cluster:
//
//	221b-sim -nodes 500 -pods 15000 -listen 127.0.0.1:7443
//	221b -api-server http://127.0.0.1:7443 -cluster-name sim
//
// POST to /sim/... changes the cluster; see the sim package.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ucesys/221b/server/internal/fakeclaude"
	"github.com/ucesys/221b/server/internal/sim"
)

func main() {
	nodes := flag.Int("nodes", 10, "number of nodes")
	pods := flag.Int("pods", 200, "number of pods")
	listen := flag.String("listen", "127.0.0.1:7443", "address to serve on")
	seed := flag.Int64("seed", 1, "random seed, for repeatable clusters")
	metrics := flag.Bool("metrics", true, "serve metrics.k8s.io node metrics")
	kubeletStats := flag.Bool("kubelet-stats", true, "serve each node's boot time through the node proxy, as the API server does")
	settingsCM := flag.String("settings-configmap", "221b/k8s-console-221b", "namespace/name of an empty ConfigMap to serve for the console's settings (empty: none)")
	problems := flag.Bool("problems", true, "include a few unhealthy nodes and pods")
	churn := flag.Duration("churn", 0, "change usage and some pods this often (0 = never)")
	claude := flag.Bool("fake-claude", true, "serve a fake Claude Messages API under /claude, for Sherlock (the console's -sherlock-api-url http://<listen>/claude)")
	claudeOnly := flag.Bool("claude-only", false, "serve only the fake Claude API, at / and /claude")
	oidc := flag.String("oidc-issuer", "", "serve a simulated OpenID Connect provider at this URL, which must be this server's address plus /oidc (for example http://127.0.0.1:7443/oidc); accounts: admin@corp.example, dev@corp.example and viewer@corp.example with the password \"password\"")
	flag.Parse()

	var handler http.Handler
	var s *sim.Sim
	if *claudeOnly {
		fake := fakeclaude.New()
		mux := http.NewServeMux()
		mux.Handle("/claude/", http.StripPrefix("/claude", fake))
		mux.Handle("/", fake)
		handler = mux
	} else {
		s = sim.New(sim.Options{Nodes: *nodes, Pods: *pods, Seed: *seed, Metrics: *metrics, KubeletStats: *kubeletStats, SettingsConfigMap: *settingsCM, Problems: *problems, OIDCIssuer: *oidc})
		handler = s
		if *claude {
			mux := http.NewServeMux()
			mux.Handle("/claude/", http.StripPrefix("/claude", fakeclaude.New()))
			mux.Handle("/", s)
			handler = mux
		}
	}
	srv := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if *churn > 0 && s != nil {
		go func() {
			t := time.NewTicker(*churn)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					s.Churn(max(1, *pods/500))
				}
			}
		}()
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	if s == nil {
		log.Printf("serving a fake Claude API on http://%s", *listen)
	} else {
		log.Printf("simulating %d nodes and %d pods on http://%s", *nodes, *pods, *listen)
	}
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
