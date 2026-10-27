package watson

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/kubectl/pkg/cmd/annotate"
	"k8s.io/kubectl/pkg/cmd/auth"
	"k8s.io/kubectl/pkg/cmd/delete"
	"k8s.io/kubectl/pkg/cmd/describe"
	"k8s.io/kubectl/pkg/cmd/drain"
	"k8s.io/kubectl/pkg/cmd/events"
	"k8s.io/kubectl/pkg/cmd/get"
	"k8s.io/kubectl/pkg/cmd/label"
	"k8s.io/kubectl/pkg/cmd/logs"
	"k8s.io/kubectl/pkg/cmd/patch"
	"k8s.io/kubectl/pkg/cmd/rollout"
	"k8s.io/kubectl/pkg/cmd/scale"
	"k8s.io/kubectl/pkg/cmd/taint"
	"k8s.io/kubectl/pkg/cmd/top"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/kube"
)

// Running kubectl inside the server (P6-03): each command is kubectl's own
// cobra command, built against a factory whose REST config is the
// console's connection to the cluster with the person's identity
// impersonated, so the API server applies their RBAC. Nothing is read
// from disk and no process is started.

// fatal is how kubectl's CheckErr ends a command: a panic the run
// recovers from, instead of os.Exit.
type fatal struct {
	msg  string
	code int
}

var fatalOnce sync.Once

// debugPanics prints the stack of a command that panicked, in tests.
var debugPanics = false

func catchFatal() {
	fatalOnce.Do(func() {
		cmdutil.BehaviorOnFatal(func(msg string, code int) { panic(fatal{msg: msg, code: code}) })
	})
}

// Target is where and as whom a command runs.
type Target struct {
	// Config is the console's connection to the cluster.
	Config *rest.Config
	// Person is who the command runs as; the zero Person runs as the
	// console's own account (sign-in off).
	Person kube.Person
	// Discovery is the cluster's cached discovery, shared across runs.
	Discovery discovery.CachedDiscoveryInterface
	// Cluster is the connection's id, and Apps the applications the
	// console keeps, for helm install and upgrade from a git source
	// (Phase 10); nil keeps none.
	Cluster string
	Apps    apps.Store
}

// Discovery caches each cluster's API discovery, which kubectl and Helm
// need to resolve "deploy" to deployments.apps. It is read as the
// console, refreshed every few minutes and when a type is not found.
type Discovery struct {
	mu    sync.Mutex
	byKey map[string]*cachedDisc
}

type cachedDisc struct {
	disc discovery.CachedDiscoveryInterface
	at   time.Time
}

// For returns the cached discovery of a cluster, by its connection ID.
func (d *Discovery) For(cluster string, cfg *rest.Config) (discovery.CachedDiscoveryInterface, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.byKey == nil {
		d.byKey = map[string]*cachedDisc{}
	}
	key := cluster + "\x00" + cfg.Host
	if c := d.byKey[key]; c != nil {
		if time.Since(c.at) > 5*time.Minute {
			c.disc.Invalidate()
			c.at = time.Now()
		}
		return c.disc, nil
	}
	dc, err := discovery.NewDiscoveryClientForConfig(rest.CopyConfig(cfg))
	if err != nil {
		return nil, err
	}
	c := &cachedDisc{disc: memory.NewMemCacheClient(dc), at: time.Now()}
	d.byKey[key] = c
	return c.disc, nil
}

// getter is the RESTClientGetter kubectl's factory and Helm read their
// clients from.
type getter struct {
	cfg       *rest.Config
	disc      discovery.CachedDiscoveryInterface
	mapper    meta.RESTMapper
	namespace string
	explicit  bool
}

func newGetter(ctx context.Context, t Target, namespace string, errOut io.Writer) *getter {
	cfg := rest.CopyConfig(t.Config)
	if !t.Person.Nobody() {
		cfg.Impersonate = rest.ImpersonationConfig{UserName: t.Person.User, Groups: t.Person.ImpersonationGroups()}
	}
	if cfg.ContentType == "" {
		// Said outright: the typed clients would otherwise prefer protobuf,
		// which the simulator does not speak. JSON prints the same.
		cfg.ContentType, cfg.AcceptContentTypes = "application/json", "application/json"
	}
	cfg.QPS, cfg.Burst = 50, 100
	cfg.WarningHandler = rest.NewWarningWriter(errOut, rest.WarningWriterOptions{Deduplicate: true})
	// A stopped command's requests fail at once.
	cfg.Wrap(func(rt http.RoundTripper) http.RoundTripper { return stoppable{rt: rt, ctx: ctx} })
	explicit := namespace != ""
	if namespace == "" {
		namespace = "default"
	}
	mapper := restmapper.NewShortcutExpander(restmapper.NewDeferredDiscoveryRESTMapper(t.Discovery), t.Discovery, func(s string) { fmt.Fprintln(errOut, "Warning: "+s) })
	return &getter{cfg: cfg, disc: t.Discovery, mapper: mapper, namespace: namespace, explicit: explicit}
}

func (g *getter) ToRESTConfig() (*rest.Config, error) { return rest.CopyConfig(g.cfg), nil }
func (g *getter) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	return g.disc, nil
}
func (g *getter) ToRESTMapper() (meta.RESTMapper, error)        { return g.mapper, nil }
func (g *getter) ToRawKubeConfigLoader() clientcmd.ClientConfig { return loader{g} }

// loader answers kubectl's questions about the kubeconfig it never has:
// the namespace, and the REST config.
type loader struct{ g *getter }

func (l loader) RawConfig() (clientcmdapi.Config, error) {
	c := clientcmdapi.NewConfig()
	c.Clusters["watson"] = &clientcmdapi.Cluster{Server: l.g.cfg.Host}
	c.AuthInfos["watson"] = &clientcmdapi.AuthInfo{}
	c.Contexts["watson"] = &clientcmdapi.Context{Cluster: "watson", AuthInfo: "watson", Namespace: l.g.namespace}
	c.CurrentContext = "watson"
	return *c, nil
}
func (l loader) ClientConfig() (*rest.Config, error) { return rest.CopyConfig(l.g.cfg), nil }
func (l loader) Namespace() (string, bool, error)    { return l.g.namespace, l.g.explicit, nil }
func (l loader) ConfigAccess() clientcmd.ConfigAccess {
	return clientcmd.NewDefaultClientConfigLoadingRules()
}

// stoppable fails requests once the command is stopped.
type stoppable struct {
	rt  http.RoundTripper
	ctx context.Context
}

func (s stoppable) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := s.ctx.Err(); err != nil {
		return nil, errStopped
	}
	return s.rt.RoundTrip(r.WithContext(mergeCtx(r.Context(), s.ctx)))
}

var errStopped = errors.New("stopped")

// mergeCtx is r's context, also done when stop is.
func mergeCtx(r, stop context.Context) context.Context {
	if r.Done() == nil {
		return stop
	}
	ctx, cancel := context.WithCancel(r)
	go func() {
		select {
		case <-stop.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx
}

// syncWriter serializes writes to w.
type syncWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// runKubectl runs one kubectl command and returns its exit code.
func runKubectl(ctx context.Context, c *Command, t Target, out, errOut io.Writer, extra ...string) (code int) {
	catchFatal()
	// kubectl writes from several goroutines in places (a drain's evictions).
	var mu sync.Mutex
	out, errOut = &syncWriter{w: out, mu: &mu}, &syncWriter{w: errOut, mu: &mu}
	// kubectl cordon and uncordon print "error: unable to cordon node ..."
	// for a node the person may not patch, and still return success. A card
	// that says done for a refused change would be wrong, so that line fails
	// the command.
	var refused atomic.Bool
	if n := c.Name(); n == "kubectl cordon" || n == "kubectl uncordon" {
		errOut = &refusalSniffer{w: errOut, hit: &refused}
	}
	g := newGetter(ctx, t, c.Namespace, errOut)
	f := cmdutil.NewFactory(g)
	streams := genericiooptions.IOStreams{In: strings.NewReader(""), Out: out, ErrOut: errOut}
	args := append(append([]string(nil), c.Args...), extra...)
	defer func() {
		if r := recover(); r != nil {
			fe, ok := r.(fatal)
			if !ok {
				fmt.Fprintf(errOut, "watson: kubectl failed: %v\n", r)
				if debugPanics {
					fmt.Fprintf(errOut, "%s\n", debug.Stack())
				}
				code = 1
				return
			}
			if fe.msg != "" {
				msg := fe.msg
				if !strings.HasSuffix(msg, "\n") {
					msg += "\n"
				}
				io.WriteString(errOut, msg)
			}
			code = fe.code
			if code == 0 {
				code = 1
			}
		}
		if ctx.Err() != nil && code == 0 {
			code = 130
		}
	}()
	if c.Name() == "kubectl auth can-i" {
		return canI(c, f, streams, args)
	}
	var cmd *cobra.Command
	switch c.Name() {
	case "kubectl get":
		cmd = get.NewCmdGet("kubectl", f, streams)
	case "kubectl describe":
		cmd = describe.NewCmdDescribe("kubectl", f, streams)
	case "kubectl logs":
		cmd = logs.NewCmdLogs(f, streams)
	case "kubectl top":
		cmd = top.NewCmdTop(f, streams)
	case "kubectl events":
		cmd = events.NewCmdEvents(g, streams)
	case "kubectl cordon":
		cmd = drain.NewCmdCordon(f, streams)
	case "kubectl uncordon":
		cmd = drain.NewCmdUncordon(f, streams)
	case "kubectl drain":
		cmd = newCmdDrain(ctx, f, streams)
	case "kubectl scale":
		cmd = scale.NewCmdScale(f, streams)
	case "kubectl rollout restart", "kubectl rollout undo":
		cmd = rollout.NewCmdRollout("kubectl", f, streams)
	case "kubectl delete":
		cmd = delete.NewCmdDelete(f, streams)
	case "kubectl label":
		cmd = label.NewCmdLabel(f, streams)
	case "kubectl annotate":
		cmd = annotate.NewCmdAnnotate("kubectl", f, streams)
	case "kubectl patch":
		cmd = patch.NewCmdPatch(f, streams)
	case "kubectl taint":
		cmd = taint.NewCmdTaint(f, streams)
	default:
		fmt.Fprintf(errOut, "watson: %s is not supported\n", c.Name())
		return 1
	}
	// Under a root, as in kubectl: some commands name their parent.
	root := &cobra.Command{Use: "kubectl", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(cmd)
	root.SetArgs(append(append([]string(nil), c.Verb...), args...))
	root.SetIn(strings.NewReader(""))
	root.SetOut(out)
	root.SetErr(errOut)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	if err := root.ExecuteContext(ctx); err != nil {
		if ctx.Err() != nil {
			return 130
		}
		fmt.Fprintf(errOut, "error: %v\n", err)
		return 1
	}
	if ctx.Err() != nil {
		return 130
	}
	if refused.Load() {
		return 1
	}
	return 0
}

// refusalSniffer passes what is written through and notes kubectl's
// "error: unable to ..." line for a node it could not change.
type refusalSniffer struct {
	w   io.Writer
	hit *atomic.Bool
}

func (r *refusalSniffer) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("error: unable to ")) {
		r.hit.Store(true)
	}
	return r.w.Write(p)
}

// canI runs kubectl auth can-i through its options: the command itself
// ends the process for a "no".
func canI(c *Command, f cmdutil.Factory, streams genericiooptions.IOStreams, args []string) int {
	o := &auth.CanIOptions{IOStreams: streams, AllNamespaces: c.Has("all-namespaces"), Quiet: c.Has("quiet"), NoHeaders: c.Has("no-headers"), List: c.Has("list"), Subresource: c.Flag("subresource")}
	cmdutil.CheckErr(o.Complete(f, c.Positionals))
	cmdutil.CheckErr(o.Validate())
	if o.List {
		cmdutil.CheckErr(o.RunAccessList())
		return 0
	}
	allowed, err := o.RunAccessCheck()
	cmdutil.CheckErr(err)
	if !allowed {
		return 1
	}
	return 0
}
