package watson

// kubectl drain, adapted from k8s.io/kubectl/pkg/cmd/drain (v0.37,
// Apache License 2.0) for a long-lived process. kubectl's own command
// builds a printer in its eviction callback, which kubectl's drain helper
// calls from one goroutine per pod, and the two share the print flags:
// harmless in a command that exits, a data race in a server. This copy
// prints through a fresh printer under a lock, and is otherwise the same
// command: the same flags, the same helper, the same output.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/cli-runtime/pkg/resource"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"
	"k8s.io/kubectl/pkg/drain"
	"k8s.io/kubectl/pkg/scheme"
)

type drainCmd struct {
	streams  genericiooptions.IOStreams
	drainer  *drain.Helper
	nodes    []*resource.Info
	mu       sync.Mutex
	warnings *printers.WarningPrinter
}

// newCmdDrain is kubectl drain, with the flags kubectl gives it.
func newCmdDrain(ctx context.Context, f cmdutil.Factory, streams genericiooptions.IOStreams) *cobra.Command {
	o := &drainCmd{streams: streams, drainer: &drain.Helper{
		Ctx:                  ctx,
		GracePeriodSeconds:   -1,
		EvictErrorRetryDelay: 5 * time.Second,
		Out:                  streams.Out,
		ErrOut:               streams.ErrOut,
		ChunkSize:            cmdutil.DefaultChunkSize,
	}}
	o.drainer.OnPodDeletionOrEvictionFinished = o.finished
	cmd := &cobra.Command{
		Use:                   "drain NODE",
		DisableFlagsInUseLine: true,
		Run: func(cmd *cobra.Command, args []string) {
			cmdutil.CheckErr(o.complete(f, cmd, args))
			cmdutil.CheckErr(o.run())
		},
	}
	d := o.drainer
	cmd.Flags().BoolVar(&d.Force, "force", d.Force, "Continue even if there are pods that do not declare a controller.")
	cmd.Flags().BoolVar(&d.IgnoreAllDaemonSets, "ignore-daemonsets", d.IgnoreAllDaemonSets, "Ignore DaemonSet-managed pods.")
	cmd.Flags().BoolVar(&d.DeleteEmptyDirData, "delete-emptydir-data", d.DeleteEmptyDirData, "Continue even if there are pods using emptyDir.")
	cmd.Flags().IntVar(&d.GracePeriodSeconds, "grace-period", d.GracePeriodSeconds, "Period of time in seconds given to each pod to terminate gracefully.")
	cmd.Flags().DurationVar(&d.Timeout, "timeout", d.Timeout, "The length of time to wait before giving up, zero means infinite")
	cmd.Flags().StringVarP(&d.PodSelector, "pod-selector", "", d.PodSelector, "Label selector to filter pods on the node")
	cmd.Flags().BoolVar(&d.DisableEviction, "disable-eviction", d.DisableEviction, "Force drain to use delete, even if eviction is supported.")
	cmd.Flags().IntVar(&d.SkipWaitForDeleteTimeoutSeconds, "skip-wait-for-delete-timeout", d.SkipWaitForDeleteTimeoutSeconds, "If pod DeletionTimestamp older than N seconds, skip waiting for the pod.")
	cmdutil.AddChunkSizeFlag(cmd, &d.ChunkSize)
	cmdutil.AddDryRunFlag(cmd)
	cmdutil.AddLabelSelectorFlagVar(cmd, &d.Selector)
	return cmd
}

// print prints an object as kubectl's name printer does ("node/x cordoned"),
// with the dry run's suffix, one line at a time.
func (o *drainCmd) print(obj runtime.Object, operation string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	pf := genericclioptions.NewPrintFlags(operation).WithTypeSetter(scheme.Scheme)
	cmdutil.PrintFlagsWithDryRunStrategy(pf, o.drainer.DryRunStrategy)
	p, err := pf.ToPrinter()
	if err != nil {
		fmt.Fprintf(o.streams.ErrOut, "error building printer: %v\n", err)
		return
	}
	_ = p.PrintObj(obj, o.streams.Out)
}

func (o *drainCmd) finished(pod *corev1.Pod, usingEviction bool, err error) {
	verb := map[[2]bool]string{{true, false}: "evicted", {true, true}: "eviction failed", {false, false}: "deleted", {false, true}: "deletion failed"}[[2]bool{usingEviction, err != nil}]
	o.print(pod, verb)
}

func (o *drainCmd) complete(f cmdutil.Factory, cmd *cobra.Command, args []string) error {
	var err error
	if len(args) == 0 && !cmd.Flags().Changed("selector") {
		return cmdutil.UsageErrorf(cmd, "USAGE: %s [flags]", cmd.Use)
	}
	if len(args) > 0 && len(o.drainer.Selector) > 0 {
		return cmdutil.UsageErrorf(cmd, "error: cannot specify both a node name and a --selector option")
	}
	if o.drainer.DryRunStrategy, err = cmdutil.GetDryRunStrategy(cmd); err != nil {
		return err
	}
	if o.drainer.Client, err = f.KubernetesClientSet(); err != nil {
		return err
	}
	if len(o.drainer.PodSelector) > 0 {
		if _, err := labels.Parse(o.drainer.PodSelector); err != nil {
			return errors.New("--pod-selector=<pod_selector> must be a valid label selector")
		}
	}
	ns, _, err := f.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return err
	}
	o.warnings = printers.NewWarningPrinter(o.streams.ErrOut, printers.WarningPrinterOptions{Color: printers.AllowsColorOutput(o.streams.ErrOut)})
	b := f.NewBuilder().
		WithScheme(scheme.Scheme, scheme.Scheme.PrioritizedVersionsAllGroups()...).
		NamespaceParam(ns).DefaultNamespace().
		RequestChunksOf(o.drainer.ChunkSize).
		ResourceNames("nodes", args...).
		SingleResourceType().
		Flatten()
	if len(o.drainer.Selector) > 0 {
		b = b.LabelSelectorParam(o.drainer.Selector).ResourceTypes("nodes")
	}
	r := b.Do()
	if err := r.Err(); err != nil {
		return err
	}
	return r.Visit(func(info *resource.Info, err error) error {
		if err != nil {
			return err
		}
		if info.Mapping.Resource.GroupResource() != (schema.GroupResource{Group: "", Resource: "nodes"}) {
			return fmt.Errorf("error: expected resource of type node, got %q", info.Mapping.Resource)
		}
		o.nodes = append(o.nodes, info)
		return nil
	})
}

func (o *drainCmd) run() error {
	o.cordon()
	drained := sets.New[string]()
	var fatal []error
	var remaining []string
	for _, info := range o.nodes {
		if err := o.evict(info); err != nil {
			fmt.Fprintf(o.streams.ErrOut, "error: unable to drain node %q due to error: %s, continuing command...\n", info.Name, err)
			if !drained.Has(info.Name) {
				fatal = append(fatal, err)
				remaining = append(remaining, info.Name)
			}
			continue
		}
		drained.Insert(info.Name)
		o.print(info.Object, "drained")
	}
	if len(remaining) > 0 {
		fmt.Fprintf(o.streams.ErrOut, "There are pending nodes to be drained:\n")
		for _, n := range remaining {
			fmt.Fprintf(o.streams.ErrOut, " %s\n", n)
		}
	}
	return utilerrors.NewAggregate(fatal)
}

func (o *drainCmd) evict(info *resource.Info) error {
	list, errs := o.drainer.GetPodsForDeletion(info.Name)
	if errs != nil {
		return utilerrors.NewAggregate(errs)
	}
	if w := list.Warnings(); w != "" {
		o.warnings.Print(w)
	}
	if o.drainer.DryRunStrategy == cmdutil.DryRunClient {
		for _, pod := range list.Pods() {
			fmt.Fprintf(o.streams.Out, "evicting pod %s/%s (dry run)\n", pod.Namespace, pod.Name)
		}
		return nil
	}
	if err := o.drainer.DeleteOrEvictPods(list.Pods()); err != nil {
		pending, newErrs := o.drainer.GetPodsForDeletion(info.Name)
		if pending != nil {
			if pods := pending.Pods(); len(pods) != 0 {
				fmt.Fprintf(o.streams.ErrOut, "There are pending pods in node %q when an error occurred: %v\n", info.Name, err)
				for _, p := range pods {
					fmt.Fprintf(o.streams.ErrOut, "%s/%s\n", "pod", p.Name)
				}
			}
		}
		if newErrs != nil {
			fmt.Fprintf(o.streams.ErrOut, "Following errors occurred while getting the list of pods to delete:\n%s", utilerrors.NewAggregate(newErrs))
		}
		return err
	}
	return nil
}

// cordon is kubectl drain's first step, as in kubectl.
func (o *drainCmd) cordon() {
	for _, info := range o.nodes {
		fail := func(err error) {
			fmt.Fprintf(o.streams.ErrOut, "error: unable to cordon node %q: %v\n", info.Name, err)
		}
		gvk := info.ResourceMapping().GroupVersionKind
		if gvk.Kind != "Node" {
			o.print(info.Object, "skipped")
			continue
		}
		c, err := drain.NewCordonHelperFromRuntimeObject(info.Object, scheme.Scheme, gvk)
		if err != nil {
			fail(err)
			continue
		}
		if !c.UpdateIfRequired(true) {
			o.print(info.Object, "already cordoned")
			continue
		}
		if o.drainer.DryRunStrategy != cmdutil.DryRunClient {
			err, patchErr := c.PatchOrReplaceWithContext(o.drainer.Ctx, o.drainer.Client, o.drainer.DryRunStrategy == cmdutil.DryRunServer)
			if patchErr != nil {
				fail(patchErr)
			}
			if err != nil {
				fail(err)
				continue
			}
		}
		o.print(info.Object, "cordoned")
	}
}
