package watson

// kubectl logs, adapted from k8s.io/kubectl/pkg/cmd/logs (v0.37, Apache
// License 2.0) for a long-lived process. kubectl's RunLogsContext runs
// under interrupt.New(nil, cancel), which subscribes to SIGTERM and SIGINT
// and ends the process with os.Exit(1): in the console that skipped its
// graceful shutdown (a helm upgrade left pending, audit events lost). This
// copy reads the logs the same way, under the command's context, without
// a signal handler. Watson never follows logs (--follow is refused), so
// only kubectl's sequential reading is kept.

import (
	"context"
	"fmt"
	"io"
	"regexp"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/client-go/rest"
	"k8s.io/kubectl/pkg/cmd/logs"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"
)

// containerOfRef is kubectl's, for --prefix.
var containerOfRef = regexp.MustCompile(`spec\.(?:initContainers|containers|ephemeralContainers){(.+)}`)

// newCmdLogs is kubectl logs, with the flags kubectl gives it.
func newCmdLogs(f cmdutil.Factory, streams genericiooptions.IOStreams) *cobra.Command {
	o := logs.NewLogsOptions(streams)
	cmd := &cobra.Command{
		Use:                   "logs [-f] [-p] (POD | TYPE/NAME) [-c CONTAINER]",
		DisableFlagsInUseLine: true,
		Run: func(cmd *cobra.Command, args []string) {
			cmdutil.CheckErr(o.Complete(f, cmd, args))
			cmdutil.CheckErr(o.Validate())
			cmdutil.CheckErr(runLogs(cmd.Context(), o))
		},
	}
	o.AddFlags(cmd)
	return cmd
}

// runLogs is RunLogsContext without the signal handler.
func runLogs(ctx context.Context, o *logs.LogsOptions) error {
	if o.Follow {
		return fmt.Errorf("watson does not follow logs")
	}
	var requests map[corev1.ObjectReference]rest.ResponseWrapper
	var err error
	if o.AllPods {
		requests, err = o.AllPodLogsForObject(o.RESTClientGetter, o.Object, o.Options, o.GetPodTimeout, o.AllContainers)
	} else {
		requests, err = o.LogsForObject(o.RESTClientGetter, o.Object, o.Options, o.GetPodTimeout, o.AllContainers)
	}
	if err != nil {
		return err
	}
	for ref, request := range requests {
		out := io.Writer(o.Out)
		if o.Prefix && ref.FieldPath != "" && ref.Name != "" {
			container := ""
			if m := containerOfRef.FindStringSubmatch(ref.FieldPath); len(m) == 2 {
				container = m[1]
			}
			out = &prefixWriter{prefix: []byte(fmt.Sprintf("[pod/%s/%s] ", ref.Name, container)), w: o.Out}
		}
		if err := o.ConsumeRequestFn(ctx, request, out); err != nil {
			if o.IgnoreLogErrors {
				fmt.Fprint(o.Out, "error: "+err.Error()+"\n")
				continue
			}
			return err
		}
	}
	return nil
}

// prefixWriter puts kubectl's --prefix before each write, which is a line.
type prefixWriter struct {
	prefix []byte
	w      io.Writer
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	n, err := p.w.Write(append(append([]byte(nil), p.prefix...), b...))
	if n > len(b) {
		n = len(b)
	}
	return n, err
}
