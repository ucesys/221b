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
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ucesys/221b/server/internal/awsv4"
	"github.com/ucesys/221b/server/internal/casework"
	"github.com/ucesys/221b/server/internal/fakeclaude"
	"github.com/ucesys/221b/server/internal/s3/s3test"
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
	// --- One flow for findings and cases (Phase 12, wave 6) ---
	seedCases := flag.Bool("seed-cases", true, "start the console's ConfigMaps with sample cases, runs, a known issue, an exception and an RCA (Phase 12)")
	problems := flag.Bool("problems", true, "include a few unhealthy nodes and pods")
	planted := flag.Bool("planted", false, "add a crash-looping pod whose annotation and log carry instructions to Sherlock, for the evaluation")
	churn := flag.Duration("churn", 0, "change usage and some pods this often (0 = never)")
	claude := flag.Bool("fake-claude", true, "serve a fake Claude Messages API under /claude, for Sherlock (the console's -sherlock-api-url http://<listen>/claude)")
	claudeOnly := flag.Bool("claude-only", false, "serve only the fake Claude API, at / and /claude")
	sshHost := flag.String("ssh-host", "", "also serve a fake SSH host outside Kubernetes at this address (for example 127.0.0.1:7522), which a console can add, pair with the account admin and the password \"host-password\", and run Automation on")
	sshHostOnly := flag.Bool("ssh-host-only", false, "serve only the fake SSH host, at -ssh-host")
	// --- Mycroft tab (Phase 12, wave 5) ---
	wikiBucket := flag.String("fake-s3-bucket", "sim-wiki", "serve a fake S3 bucket of this name, path-style at http://<listen>/<bucket>, for Mycroft's wiki (region us-east-1, access key \"sim-access-key\", secret \"sim-secret-key\"); empty: none")
	oidc := flag.String("oidc-issuer", "", "serve a simulated OpenID Connect provider at this URL, which must be this server's address plus /oidc (for example http://127.0.0.1:7443/oidc); accounts: admin@corp.example, dev@corp.example and viewer@corp.example with the password \"password\"")
	flag.Parse()

	if *sshHost != "" {
		h, err := sim.NewSSHHost()
		if err != nil {
			log.Fatal(err)
		}
		ln, err := net.Listen("tcp", *sshHost)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("serving a fake SSH host on %s, host key %s", ln.Addr(), h.Fingerprint())
		if *sshHostOnly {
			log.Fatal(h.Serve(ln))
		}
		go h.Serve(ln)
	}

	var handler http.Handler
	var s *sim.Sim
	if *claudeOnly {
		fake := fakeclaude.New()
		mux := http.NewServeMux()
		mux.Handle("/claude/", http.StripPrefix("/claude", fake))
		mux.Handle("/", fake)
		handler = mux
	} else {
		s = sim.New(sim.Options{Nodes: *nodes, Pods: *pods, Seed: *seed, Metrics: *metrics, KubeletStats: *kubeletStats, SettingsConfigMap: *settingsCM, Problems: *problems, Planted: *planted, OIDCIssuer: *oidc, Preload: preload(*seedCases)})
		handler = s
		if *claude || *wikiBucket != "" {
			mux := http.NewServeMux()
			if *claude {
				mux.Handle("/claude/", http.StripPrefix("/claude", fakeclaude.New()))
			}
			if *wikiBucket != "" {
				bucket := s3test.NewHandler(*wikiBucket, "us-east-1", awsv4.Credentials{AccessKeyID: "sim-access-key", SecretAccessKey: "sim-secret-key"})
				mux.Handle("/"+*wikiBucket, bucket.Handler())
				mux.Handle("/"+*wikiBucket+"/", bucket.Handler())
			}
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

// preload is the sample cases the console starts with (Phase 12, wave 6):
// the case analysis's and the routing's own records, made by
// casework.MakeSeed, in the ConfigMaps the console reads and one of its
// own per case, labelled as the console labels them.
func preload(on bool) func(name string) (map[string]map[string]string, []sim.SeedObject) {
	if !on {
		return nil
	}
	return func(name string) (map[string]map[string]string, []sim.SeedObject) {
		seed, err := casework.MakeSeed(time.Now())
		if err != nil {
			log.Fatalf("the sample cases: %v", err)
		}
		var objs []sim.SeedObject
		for n, keys := range seed.Cases {
			objs = append(objs, sim.SeedObject{Name: name + "-case-" + n, Data: keys,
				Labels: map[string]string{"app.kubernetes.io/instance": name, "app.kubernetes.io/managed-by": "221b", "221b.io/case": n}})
		}
		return map[string]map[string]string{name + "-issues": seed.Issues, name + "-casework": seed.Casework, name + "-runs": seed.Runs}, objs
	}
}
