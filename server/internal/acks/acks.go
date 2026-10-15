// Package acks keeps acknowledged alerts (asked at the Phase 4 review).
// Someone who has seen an alert acknowledges it: it leaves the banners and
// the Alerts tab's count for everyone, and stays on the Alerts tab marked
// with who acknowledged it and when. An acknowledgement holds while the
// alert stays as it was: when the alert changes (a pod fails another way,
// a node gets another problem) or clears, it lapses.
package acks

import (
	"fmt"
	"sort"
	"time"
)

// Ack is one acknowledged alert.
type Ack struct {
	// Key is the alert's key: "pod:web/api-0", "node:worker-1".
	Key string `json:"key"`
	// Fingerprint is what the alert was when it was acknowledged.
	Fingerprint string `json:"fp"`
	// By names who acknowledged it: their email, name or username.
	By string    `json:"by"`
	At time.Time `json:"at"`
}

// Max keeps the settings small: more acknowledged alerts than this at once
// means the cluster has bigger problems than its banners.
const Max = 500

// Validate reports what is wrong with a cluster's acknowledgements.
func Validate(list []Ack) error {
	if len(list) > Max {
		return fmt.Errorf("at most %d acknowledged alerts", Max)
	}
	seen := map[string]bool{}
	for _, a := range list {
		switch {
		case a.Key == "" || len(a.Key) > 600:
			return fmt.Errorf("an acknowledgement needs the alert's key")
		case a.Fingerprint == "" || len(a.Fingerprint) > 2000:
			return fmt.Errorf("the acknowledgement of %s has no fingerprint", a.Key)
		case len(a.By) > 320:
			return fmt.Errorf("the acknowledgement of %s names too long an identity", a.Key)
		case seen[a.Key]:
			return fmt.Errorf("%s is acknowledged twice", a.Key)
		}
		seen[a.Key] = true
	}
	return nil
}

// Set returns the list with the alert acknowledged as given, replacing an
// earlier acknowledgement of the same alert.
func Set(list []Ack, a Ack) []Ack {
	out := Remove(list, a.Key)
	out = append(out, a)
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Remove returns the list without the alert's acknowledgement.
func Remove(list []Ack, key string) []Ack {
	out := make([]Ack, 0, len(list))
	for _, a := range list {
		if a.Key != key {
			out = append(out, a)
		}
	}
	return out
}

// Prune drops the acknowledgements that have lapsed: their alert is gone
// or is no longer what was acknowledged. now tells what each alert is now:
// its fingerprint, or ok=false when it has none.
func Prune(list []Ack, now func(key string) (fingerprint string, ok bool)) []Ack {
	out := make([]Ack, 0, len(list))
	for _, a := range list {
		if fp, ok := now(a.Key); ok && fp == a.Fingerprint {
			out = append(out, a)
		}
	}
	return out
}

// Index maps a list by alert key.
func Index(list []Ack) map[string]Ack {
	out := make(map[string]Ack, len(list))
	for _, a := range list {
		out[a.Key] = a
	}
	return out
}
