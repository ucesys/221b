package watson

// kubectl delete's wait, in Watson: kubectl waits for the objects it
// deletes with RunWaitContext(context.Background()), a week at most, under
// a signal handler that ends the process at SIGTERM. Watson runs kubectl
// delete with --wait=false and waits here instead, under the command's
// context: Stop ends the wait, and the console's shutdown handles the
// signal. As in kubectl, an object is gone when it is not found or another
// object of its name has another UID, and a wait the person may not watch
// ends quietly.

import (
	"context"
	"fmt"
	"slices"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"
)

// deleted is an object a delete waits for.
type deleted struct {
	gvr       schema.GroupVersionResource
	namespace string
	name      string
	uid       types.UID
}

// waitsForDeletion says whether kubectl would wait: not on a dry run, and
// not with --wait=false.
func waitsForDeletion(c *Command, extra []string) bool {
	if c.DryRun() || c.Flag("wait") == "false" {
		return false
	}
	return !slices.ContainsFunc(extra, func(e string) bool { return len(e) >= 9 && e[:9] == "--dry-run" })
}

// deleteTimeout is --timeout, or kubectl's week when none is given.
func deleteTimeout(c *Command) time.Duration {
	if d, err := time.ParseDuration(c.Flag("timeout")); err == nil && d > 0 {
		return d
	}
	return 168 * time.Hour
}

// resolveDeleted reads the objects a delete names, before it runs, as
// kubectl's builder does: by name or by selector, in the command's
// namespace. Those it cannot read are not waited for; kubectl reports them.
func resolveDeleted(f cmdutil.Factory, c *Command) []deleted {
	ns, _, err := f.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return nil
	}
	b := f.NewBuilder().Unstructured().ContinueOnError().NamespaceParam(ns).DefaultNamespace().ResourceTypeOrNameArgs(false, c.Positionals...).Flatten()
	if sel := c.Flag("selector"); sel != "" {
		b = b.LabelSelectorParam(sel)
	}
	infos, _ := b.Do().IgnoreErrors(apierrors.IsNotFound).Infos()
	var out []deleted
	for _, info := range infos {
		d := deleted{gvr: info.Mapping.Resource, name: info.Name}
		if info.Namespaced() {
			d.namespace = info.Namespace
		}
		if o, ok := info.Object.(metav1.Object); ok {
			d.uid = o.GetUID()
		}
		out = append(out, d)
	}
	return out
}

// waitDeleted waits until every object is gone, ctx ends or the timeout
// passes.
func waitDeleted(ctx context.Context, f cmdutil.Factory, objs []deleted, timeout time.Duration) error {
	dyn, err := f.DynamicClient()
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for _, o := range objs {
		for {
			gone, err := isGone(ctx, dyn, o)
			if gone || apierrors.IsForbidden(err) || apierrors.IsMethodNotSupported(err) {
				break
			}
			select {
			case <-ctx.Done():
				if ctx.Err() == context.DeadlineExceeded {
					return fmt.Errorf("timed out waiting for the condition on %s/%s", o.gvr.Resource, o.name)
				}
				return ctx.Err()
			case <-tick.C:
			}
		}
	}
	return nil
}

func isGone(ctx context.Context, dyn dynamic.Interface, o deleted) (bool, error) {
	var ri dynamic.ResourceInterface = dyn.Resource(o.gvr)
	if o.namespace != "" {
		ri = dyn.Resource(o.gvr).Namespace(o.namespace)
	}
	got, err := ri.Get(ctx, o.name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return o.uid != "" && got.GetUID() != o.uid, nil
}
