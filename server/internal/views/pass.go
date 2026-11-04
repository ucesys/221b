package views

import corev1 "k8s.io/api/core/v1"

// Pass is a Source for one pass over a view's rows. Rows of a pass share
// what each would otherwise work out again from the whole cluster: a
// namespace's budgets and the pods they select, the Ingress controllers'
// pods. It lives for that one pass, on one goroutine; a row built from the
// plain Source works the same out for itself.
type Pass struct {
	Source
	budgets     map[string][]parsedBudget
	budgetPods  map[string]map[string][]budgetPod
	controllers map[string][]*corev1.Pod
}

// NewPass starts a pass over src.
func NewPass(src Source) *Pass {
	return &Pass{Source: src, budgets: map[string][]parsedBudget{}, budgetPods: map[string]map[string][]budgetPod{}}
}
