// Package kubetest gives a test a cluster that is not there.
package kubetest

import (
	"errors"
	"strconv"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// Cluster is a cluster that is not there whose Secrets have versions as an
// API server gives them: each write gets the next one, and an update over
// a version that is not the Secret's is refused as a conflict. The plain
// fake checks nothing and gives no versions, so a store that holds a
// Secret to its version (the applications', the connections') is tested
// over this one.
func Cluster() *fake.Clientset {
	cs := fake.NewSimpleClientset()
	var mu sync.Mutex
	version := 0
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	cs.PrependReactor("create", "secrets", func(a k8stesting.Action) (bool, runtime.Object, error) {
		mu.Lock()
		defer mu.Unlock()
		version++
		a.(k8stesting.CreateAction).GetObject().(*corev1.Secret).ResourceVersion = strconv.Itoa(version)
		return false, nil, nil
	})
	cs.PrependReactor("update", "secrets", func(a k8stesting.Action) (bool, runtime.Object, error) {
		mu.Lock()
		defer mu.Unlock()
		sec := a.(k8stesting.UpdateAction).GetObject().(*corev1.Secret)
		cur, err := cs.Tracker().Get(gvr, a.GetNamespace(), sec.Name)
		if err != nil {
			return false, nil, nil
		}
		if was := cur.(*corev1.Secret).ResourceVersion; sec.ResourceVersion != "" && sec.ResourceVersion != was {
			return true, nil, apierrors.NewConflict(gvr.GroupResource(), sec.Name, errors.New("the object has been modified"))
		}
		version++
		sec.ResourceVersion = strconv.Itoa(version)
		return false, nil, nil
	})
	return cs
}
