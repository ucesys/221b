package automation

import (
	"context"
	"fmt"
	"sort"
)

// Parameter limits (AU-06).
const (
	MaxValue        = 1 << 10
	MaxParams       = 100
	maxParamsBytes  = 64 << 10
	SourceRun       = "run"
	SourceNode      = "node"
	SourceFleet     = "fleet"
	SourceMissing   = ""
	defaultCeiling  = PrivilegeFull
	maxNodeOverride = 500
)

// FleetParams are a fleet's parameter values, its nodes' overrides and the
// most privilege a run on it may get.
type FleetParams struct {
	Values map[string]string            `json:"values,omitempty"`
	Nodes  map[string]map[string]string `json:"nodes,omitempty"`
	// Ceiling is none or full; empty is full.
	Ceiling string `json:"ceiling,omitempty"`
}

// CeilingOrDefault is the fleet's ceiling, full when unset.
func (fp FleetParams) CeilingOrDefault() string {
	if fp.Ceiling == "" {
		return defaultCeiling
	}
	return fp.Ceiling
}

// Allows says whether a privilege is within the ceiling.
func (fp FleetParams) Allows(privilege string) bool {
	return fp.CeilingOrDefault() == PrivilegeFull || privilege == PrivilegeNone
}

func checkValues(where string, m map[string]string) error {
	if len(m) > MaxParams {
		return fmt.Errorf("%w: %s has more than %d parameters", ErrInvalid, where, MaxParams)
	}
	for k, v := range m {
		if !ParamNameRE.MatchString(k) {
			return fmt.Errorf("%w: %s is not a parameter name (capitals, digits and underscores)", ErrInvalid, k)
		}
		if Reserved[k] {
			return fmt.Errorf("%w: %s is set by the console on every node", ErrInvalid, k)
		}
		if len(v) > MaxValue {
			return fmt.Errorf("%w: %s's value in %s is over %d bytes", ErrInvalid, k, where, MaxValue)
		}
	}
	return nil
}

// ValidateParams checks a fleet's parameters (AU-06).
func ValidateParams(fp FleetParams) error {
	if fp.Ceiling != "" && fp.Ceiling != PrivilegeNone && fp.Ceiling != PrivilegeFull {
		return fmt.Errorf("%w: the ceiling is none or full", ErrInvalid)
	}
	if err := checkValues("the fleet", fp.Values); err != nil {
		return err
	}
	if len(fp.Nodes) > maxNodeOverride {
		return fmt.Errorf("%w: at most %d nodes may override values", ErrInvalid, maxNodeOverride)
	}
	for node, m := range fp.Nodes {
		if err := checkValues("node "+node, m); err != nil {
			return err
		}
	}
	return nil
}

// ValidateOverrides checks a run's own values.
func ValidateOverrides(m map[string]string) error { return checkValues("the run", m) }

type paramsRecord struct {
	// Clusters holds each cluster's fleets' parameters, by fleet id.
	Clusters map[string]map[string]FleetParams `json:"clusters"`
}

// Params returns a cluster's fleets' parameters, by fleet id.
func (st *Store) Params(ctx context.Context, cluster string) (map[string]FleetParams, error) {
	var rec paramsRecord
	if _, err := st.load(ctx, ParamsKey, &rec); err != nil {
		return nil, err
	}
	out := rec.Clusters[cluster]
	if out == nil {
		out = map[string]FleetParams{}
	}
	return out, nil
}

// SetParams replaces a fleet's parameters.
func (st *Store) SetParams(ctx context.Context, cluster, fleet string, fp FleetParams) error {
	if err := ValidateParams(fp); err != nil {
		return err
	}
	for node, m := range fp.Nodes {
		if len(m) == 0 {
			delete(fp.Nodes, node)
		}
	}
	return change(ctx, st, ParamsKey, maxParamsBytes, func(rec *paramsRecord) error {
		if rec.Clusters == nil {
			rec.Clusters = map[string]map[string]FleetParams{}
		}
		if rec.Clusters[cluster] == nil {
			rec.Clusters[cluster] = map[string]FleetParams{}
		}
		if len(fp.Values) == 0 && len(fp.Nodes) == 0 && fp.Ceiling == "" {
			delete(rec.Clusters[cluster], fleet)
		} else {
			rec.Clusters[cluster][fleet] = fp
		}
		return nil
	})
}

// Resolved is one node's values for a run: each declared parameter's value
// and where it came from.
type Resolved struct {
	Values  map[string]string `json:"values"`
	Sources map[string]string `json:"sources"`
	Missing []string          `json:"missing,omitempty"`
}

// Resolve works out a node's values for the parameters a script declares
// (AU-06): the run's own, then the node's, then the fleet's. A required
// parameter with none is missing.
func Resolve(h Header, fp FleetParams, node string, run map[string]string) Resolved {
	r := Resolved{Values: map[string]string{}, Sources: map[string]string{}}
	for _, p := range h.Params {
		if v, ok := run[p.Name]; ok {
			r.Values[p.Name], r.Sources[p.Name] = v, SourceRun
		} else if v, ok := fp.Nodes[node][p.Name]; ok {
			r.Values[p.Name], r.Sources[p.Name] = v, SourceNode
		} else if v, ok := fp.Values[p.Name]; ok {
			r.Values[p.Name], r.Sources[p.Name] = v, SourceFleet
		} else if p.Required {
			r.Missing = append(r.Missing, p.Name)
		}
	}
	sort.Strings(r.Missing)
	return r
}
