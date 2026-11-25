package kube

import (
	"context"
	"testing"
	"time"
)

// A read of the volumes whose time is up hands out no node (kube-cut-3):
// with a worker waiting, a select with both cases ready picks either, so a
// node could be handed out after the deadline, counted as read and
// skipped by the next read.
func TestAReadOutOfTimeHandsOutNoNode(t *testing.T) {
	spent, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	for range 200 {
		work := make(chan string)
		got := make(chan int)
		go func() {
			n := 0
			for range work {
				n++
			}
			got <- n
		}()
		// The worker waits on work before the nodes are handed out.
		time.Sleep(50 * time.Microsecond)
		sent := handOut(spent, []string{"a", "b", "c"}, work)
		close(work)
		if n := <-got; sent != 0 || n != 0 {
			t.Fatalf("%d nodes handed out after the deadline (%d taken)", sent, n)
		}
	}
}
