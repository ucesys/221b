package views

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	resourcehelper "k8s.io/component-helpers/resource"
)

// Quotas and limit ranges (P4-06): what each ResourceQuota has used of
// what it allows, as the API server keeps count, and whether a rolling
// update of each Deployment in its namespace has room for its surge pods.

// QuotaItem is one resource of a quota.
type QuotaItem struct {
	Resource string  `json:"resource"`
	Used     string  `json:"used"`
	Hard     string  `json:"hard"`
	Pct      float64 `json:"pct"`
	Tone     string  `json:"tone"`
	// UsedValue and HardValue are in millicores for CPU, bytes for
	// memory and storage, and units otherwise.
	UsedValue float64 `json:"usedValue"`
	HardValue float64 `json:"hardValue"`
}

// QuotaRow is a ResourceQuota.
type QuotaRow struct {
	Key       string      `json:"key"`
	Namespace string      `json:"namespace"`
	Name      string      `json:"name"`
	Items     []QuotaItem `json:"items"`
	Scopes    []string    `json:"scopes"`
	Release   string      `json:"release,omitempty"`
	Created   *time.Time  `json:"created,omitempty"`
}

var quotaOrder = map[string]int{"requests.cpu": 0, "cpu": 0, "requests.memory": 1, "memory": 1, "limits.cpu": 2, "limits.memory": 3, "pods": 4, "count/pods": 4}

func quotaKeys(hard corev1.ResourceList) []string {
	keys := make([]string, 0, len(hard))
	for k := range hard {
		keys = append(keys, string(k))
	}
	sort.Slice(keys, func(i, j int) bool {
		a, aok := quotaOrder[keys[i]]
		b, bok := quotaOrder[keys[j]]
		switch {
		case aok && bok:
			return a < b
		case aok != bok:
			return aok
		}
		return keys[i] < keys[j]
	})
	return keys
}

func qValue(name string, q resource.Quantity) float64 {
	if strings.HasSuffix(name, "cpu") {
		return float64(q.MilliValue())
	}
	return q.AsApproximateFloat64()
}

func quotaRow(src Source, ns, name string, now time.Time) (any, bool) {
	q, _ := src.Object("resourcequotas", ns, name).(*corev1.ResourceQuota)
	if q == nil {
		return nil, false
	}
	r := QuotaRow{Key: PolQuota + ":" + ns + "/" + name, Namespace: ns, Name: name, Items: []QuotaItem{}, Scopes: []string{}, Release: objectRelease(q), Created: created(q)}
	hard := q.Status.Hard
	if len(hard) == 0 {
		hard = q.Spec.Hard
	}
	for _, k := range quotaKeys(hard) {
		h := hard[corev1.ResourceName(k)]
		u := q.Status.Used[corev1.ResourceName(k)]
		it := QuotaItem{Resource: k, Used: u.String(), Hard: h.String(), UsedValue: qValue(k, u), HardValue: qValue(k, h)}
		switch {
		case it.HardValue > 0:
			it.Pct = math.Round(it.UsedValue/it.HardValue*1000) / 10
		case it.UsedValue > 0:
			it.Pct = 100
		}
		switch {
		case it.Pct >= 90:
			it.Tone = "err"
		case it.Pct >= 75:
			it.Tone = "warn"
		default:
			it.Tone = "ok"
		}
		r.Items = append(r.Items, it)
	}
	for _, s := range q.Spec.Scopes {
		r.Scopes = append(r.Scopes, string(s))
	}
	if q.Spec.ScopeSelector != nil {
		for _, e := range q.Spec.ScopeSelector.MatchExpressions {
			r.Scopes = append(r.Scopes, fmt.Sprintf("%s %s %s", e.ScopeName, e.Operator, strings.Join(e.Values, ", ")))
		}
	}
	_ = now
	return r, true
}

// RolloutShort is a quota resource a rolling update runs out of.
type RolloutShort struct {
	Quota    string `json:"quota"`
	Resource string `json:"resource"`
	Need     string `json:"need"`
	Left     string `json:"left"`
}

// RolloutRow is a Deployment in a namespace with a quota: whether a
// rolling update has room for the pods it adds before it removes any.
type RolloutRow struct {
	Key         string `json:"key"`
	Namespace   string `json:"namespace"`
	Name        string `json:"name"`
	Replicas    int32  `json:"replicas"`
	Strategy    string `json:"strategy"`
	Surge       int    `json:"surge"`
	Unavailable int    `json:"unavailable"`
	// Requests and Limits are what one of its pods asks for.
	Requests map[string]string `json:"requests"`
	Limits   map[string]string `json:"limits"`
	Short    []RolloutShort    `json:"short"`
	// Stalls says the update would wait for room it never gets: it must
	// add a pod before removing one, and a quota has no room for it.
	Stalls bool `json:"stalls"`
}

func rolloutRow(src Source, ns, name string) (any, bool) {
	d, _ := src.Object("deployments", ns, name).(*appsv1.Deployment)
	if d == nil {
		return nil, false
	}
	quotas := []*corev1.ResourceQuota{}
	for _, o := range src.Objects("resourcequotas", ns) {
		if q, ok := o.(*corev1.ResourceQuota); ok {
			quotas = append(quotas, q)
		}
	}
	if len(quotas) == 0 {
		return nil, false
	}
	sort.Slice(quotas, func(i, j int) bool { return quotas[i].Name < quotas[j].Name })
	replicas := int32(1)
	if d.Spec.Replicas != nil {
		replicas = *d.Spec.Replicas
	}
	r := RolloutRow{Key: PolRollout + ":" + ns + "/" + name, Namespace: ns, Name: name, Replicas: replicas, Strategy: string(d.Spec.Strategy.Type), Requests: map[string]string{}, Limits: map[string]string{}, Short: []RolloutShort{}}
	if r.Strategy == "" {
		r.Strategy = string(appsv1.RollingUpdateDeploymentStrategyType)
	}
	if r.Strategy == string(appsv1.RecreateDeploymentStrategyType) {
		r.Unavailable = int(replicas)
	} else {
		surge, unavail := "25%", "25%"
		if ru := d.Spec.Strategy.RollingUpdate; ru != nil {
			if ru.MaxSurge != nil {
				surge = ru.MaxSurge.String()
			}
			if ru.MaxUnavailable != nil {
				unavail = ru.MaxUnavailable.String()
			}
		}
		r.Surge = scaled(surge, int(replicas), true)
		r.Unavailable = scaled(unavail, int(replicas), false)
		if r.Surge == 0 && r.Unavailable == 0 {
			r.Unavailable = 1
		}
	}
	pod := &corev1.Pod{Spec: d.Spec.Template.Spec}
	req := resourcehelper.PodRequests(pod, resourcehelper.PodResourcesOptions{})
	lim := resourcehelper.PodLimits(pod, resourcehelper.PodResourcesOptions{})
	for k, v := range req {
		r.Requests[string(k)] = v.String()
	}
	for k, v := range lim {
		r.Limits[string(k)] = v.String()
	}
	need := func(key string) (resource.Quantity, bool) {
		switch key {
		case "pods", "count/pods":
			return *resource.NewQuantity(1, resource.DecimalSI), true
		case "requests.cpu", "cpu":
			q, ok := req[corev1.ResourceCPU]
			return q, ok
		case "requests.memory", "memory":
			q, ok := req[corev1.ResourceMemory]
			return q, ok
		case "limits.cpu":
			q, ok := lim[corev1.ResourceCPU]
			return q, ok
		case "limits.memory":
			q, ok := lim[corev1.ResourceMemory]
			return q, ok
		}
		return resource.Quantity{}, false
	}
	for _, q := range quotas {
		if !quotaApplies(q, &d.Spec.Template.Spec) {
			continue
		}
		hard := q.Status.Hard
		if len(hard) == 0 {
			hard = q.Spec.Hard
		}
		for _, k := range quotaKeys(hard) {
			per, ok := need(k)
			if !ok || per.IsZero() || r.Surge == 0 {
				continue
			}
			total := per.DeepCopy()
			for i := 1; i < r.Surge; i++ {
				total.Add(per)
			}
			used := q.Status.Used[corev1.ResourceName(k)]
			h := hard[corev1.ResourceName(k)]
			after := used.DeepCopy()
			after.Add(total)
			if after.Cmp(h) > 0 {
				left := h.DeepCopy()
				left.Sub(used)
				if left.Sign() < 0 {
					left = resource.Quantity{}
				}
				r.Short = append(r.Short, RolloutShort{Quota: q.Name, Resource: k, Need: total.String(), Left: left.String()})
			}
		}
	}
	r.Stalls = len(r.Short) > 0 && r.Unavailable == 0 && replicas > 0
	return r, true
}

// scaled resolves maxSurge (rounded up) or maxUnavailable (rounded down)
// against the replicas, as the Deployment controller does.
func scaled(v string, replicas int, up bool) int {
	if p, ok := strings.CutSuffix(v, "%"); ok {
		var n float64
		fmt.Sscanf(p, "%g", &n)
		x := n * float64(replicas) / 100
		if up {
			return int(math.Ceil(x))
		}
		return int(math.Floor(x))
	}
	var n int
	fmt.Sscanf(v, "%d", &n)
	return n
}

// quotaApplies says whether a quota counts pods like the template's: its
// scopes (BestEffort, NotBestEffort, Terminating, NotTerminating and
// PriorityClass) all match.
func quotaApplies(q *corev1.ResourceQuota, spec *corev1.PodSpec) bool {
	besteffort := true
	for _, c := range append(append([]corev1.Container{}, spec.Containers...), spec.InitContainers...) {
		if len(c.Resources.Requests) > 0 || len(c.Resources.Limits) > 0 {
			besteffort = false
		}
	}
	terminating := spec.ActiveDeadlineSeconds != nil
	match := func(scope corev1.ResourceQuotaScope, op corev1.ScopeSelectorOperator, values []string) bool {
		switch scope {
		case corev1.ResourceQuotaScopeBestEffort:
			return besteffort
		case corev1.ResourceQuotaScopeNotBestEffort:
			return !besteffort
		case corev1.ResourceQuotaScopeTerminating:
			return terminating
		case corev1.ResourceQuotaScopeNotTerminating:
			return !terminating
		case corev1.ResourceQuotaScopePriorityClass:
			has := false
			for _, v := range values {
				if v == spec.PriorityClassName {
					has = true
				}
			}
			switch op {
			case corev1.ScopeSelectorOpIn:
				return has
			case corev1.ScopeSelectorOpNotIn:
				return !has
			case corev1.ScopeSelectorOpExists:
				return spec.PriorityClassName != ""
			case corev1.ScopeSelectorOpDoesNotExist:
				return spec.PriorityClassName == ""
			}
		}
		return false
	}
	for _, s := range q.Spec.Scopes {
		if !match(s, corev1.ScopeSelectorOpExists, nil) {
			return false
		}
	}
	if q.Spec.ScopeSelector != nil {
		for _, e := range q.Spec.ScopeSelector.MatchExpressions {
			if !match(e.ScopeName, e.Operator, e.Values) {
				return false
			}
		}
	}
	return true
}

// LimitItem is one limit of a LimitRange.
type LimitItem struct {
	Type           string            `json:"type"`
	DefaultRequest map[string]string `json:"defaultRequest,omitempty"`
	Default        map[string]string `json:"default,omitempty"`
	Max            map[string]string `json:"max,omitempty"`
	Min            map[string]string `json:"min,omitempty"`
	Ratio          map[string]string `json:"maxLimitRequestRatio,omitempty"`
}

// LimitsRow is a LimitRange, with the containers whose requests and limits
// are exactly its defaults, which it most likely filled in.
type LimitsRow struct {
	Key        string      `json:"key"`
	Namespace  string      `json:"namespace"`
	Name       string      `json:"name"`
	Items      []LimitItem `json:"items"`
	AtDefaults []string    `json:"atDefaults"`
	Created    *time.Time  `json:"created,omitempty"`
}

func rl(l corev1.ResourceList) map[string]string {
	if len(l) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, v := range l {
		out[string(k)] = v.String()
	}
	return out
}

func sameList(a, b corev1.ResourceList) bool {
	if len(b) == 0 {
		return true
	}
	for k, v := range b {
		x, ok := a[k]
		if !ok || x.Cmp(v) != 0 {
			return false
		}
	}
	return true
}

func limitsRow(src Source, ns, name string, now time.Time) (any, bool) {
	l, _ := src.Object("limitranges", ns, name).(*corev1.LimitRange)
	if l == nil {
		return nil, false
	}
	r := LimitsRow{Key: PolLimits + ":" + ns + "/" + name, Namespace: ns, Name: name, Items: []LimitItem{}, AtDefaults: []string{}, Created: created(l)}
	var ctr *corev1.LimitRangeItem
	for i, it := range l.Spec.Limits {
		r.Items = append(r.Items, LimitItem{Type: string(it.Type), DefaultRequest: rl(it.DefaultRequest), Default: rl(it.Default), Max: rl(it.Max), Min: rl(it.Min), Ratio: rl(it.MaxLimitRequestRatio)})
		if it.Type == corev1.LimitTypeContainer && (len(it.Default) > 0 || len(it.DefaultRequest) > 0) {
			ctr = &l.Spec.Limits[i]
		}
	}
	if ctr != nil {
		seen := map[string]bool{}
		for _, p := range podsIn(src, ns) {
			if !live(p) {
				continue
			}
			for _, c := range append(append([]corev1.Container{}, p.Spec.InitContainers...), p.Spec.Containers...) {
				if sameList(c.Resources.Requests, ctr.DefaultRequest) && sameList(c.Resources.Limits, ctr.Default) {
					k := c.Name + " (" + workloadName(src, p) + ")"
					if !seen[k] && len(r.AtDefaults) < 24 {
						seen[k] = true
						r.AtDefaults = append(r.AtDefaults, k)
					}
				}
			}
		}
	}
	_ = now
	return r, true
}
