package apps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
)

// The secret part of an application (Phase 13, DB-62). With a database the
// record is a row of applications, and what people type into it stays in
// the application's Secret, under the key "set.json": the values file, the
// parameters and each command's parameters, which hold passwords among
// the rest. The Secret keeps its name and its labels, and its "token" key
// when it has one; "spec.json", the whole record of before, leaves it.
// Nothing in this file is given the database.

const (
	// keySet is the key the secret part is kept under.
	keySet = "set.json"
	// LabelAppNamespace and LabelAppName say which application a Secret is
	// of, now that its record is not in it: its name alone is a hash.
	LabelAppNamespace = "221b.io/app-namespace"
	LabelAppName      = "221b.io/app-name"
)

// secretPart is what "set.json" holds.
type secretPart struct {
	Values   string        `json:"values,omitempty"`
	Params   []string      `json:"params,omitempty"`
	Commands []commandPart `json:"commands,omitempty"`
}

// commandPart is a command's parameters, beside its name: the name is
// what pairs them with the command's row.
type commandPart struct {
	Name   string   `json:"name"`
	Params []string `json:"params"`
}

// partOf is the secret part of a record.
func partOf(r Record) secretPart {
	p := secretPart{Values: r.Values, Params: r.Params}
	for _, c := range r.Commands {
		p.Commands = append(p.Commands, commandPart{Name: c.Name, Params: c.Params})
	}
	return p
}

// into puts the secret part into a record read from its row. A command
// takes the parameters kept under its name, the first of them not taken
// yet: one command's parameters are never given to another, whatever the
// two halves hold, and a command whose name the Secret does not have reads
// with none.
func (p secretPart) into(r *Record) {
	r.Values, r.Params = p.Values, p.Params
	taken := make([]bool, len(p.Commands))
	for i := range r.Commands {
		r.Commands[i].Params = nil
		for j, c := range p.Commands {
			if !taken[j] && c.Name == r.Commands[i].Name {
				r.Commands[i].Params, taken[j] = c.Params, true
				break
			}
		}
	}
}

// readPart is the secret part a Secret holds. Until a Secret of before the
// move is rewritten it is read from the record in "spec.json", so nothing
// is lost when the rewrite is late. ok is false when the Secret holds
// neither in a form this console reads.
func readPart(sec *corev1.Secret) (p secretPart, ok bool) {
	if b, has := sec.Data[keySet]; has {
		// The error's text may quote what the key holds: it is not passed on.
		if json.Unmarshal(b, &p) != nil {
			return secretPart{}, false
		}
		return p, true
	}
	if b := sec.Data[keySpec]; len(b) > 0 {
		if r, err := unmarshal(b); err == nil {
			return partOf(r), true
		}
	}
	return secretPart{}, false
}

// NewSecretStoreOn makes a store over a client's Secrets of the namespace.
func NewSecretStoreOn(secrets corev1client.SecretInterface, namespace, prefix string) *SecretStore {
	return &SecretStore{secrets: secrets, namespace: namespace, prefix: prefix}
}

// labelsOf are a Secret's labels: what lists it, its cluster, and the
// application's namespace and name where a label can hold them.
func labelsOf(r Record) map[string]string {
	out := map[string]string{LabelComponent: ComponentValue}
	for label, value := range map[string]string{LabelCluster: r.Cluster, LabelAppNamespace: r.Namespace, LabelAppName: r.Name} {
		if value != "" && len(validation.IsValidLabelValue(value)) == 0 {
			out[label] = value
		}
	}
	return out
}

// all are the applications' Secrets, by name.
func (s *SecretStore) all(ctx context.Context) (map[string]*corev1.Secret, error) {
	list, err := s.list(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*corev1.Secret, len(list.Items))
	for i := range list.Items {
		out[list.Items[i].Name] = &list.Items[i]
	}
	return out, nil
}

// one is an application's Secret, nil when there is none.
func (s *SecretStore) one(ctx context.Context, cluster, namespace, name string) (*corev1.Secret, error) {
	sec, err := s.secrets.Get(ctx, s.name(cluster, namespace, name), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the application Secret: %w", err)
	}
	return sec, nil
}

// putPart writes a record's secret part to its Secret, which then holds
// "set.json", the token when there is one, and no "spec.json": a nil token
// keeps the saved one, an empty one removes it. fresh says the record is
// new: whatever a Secret of that name still held, of an application since
// removed, goes. The Secret is written over the version read, and read
// again when it changed meanwhile; one that already holds all of it is
// left alone.
func (s *SecretStore) putPart(ctx context.Context, r Record, token *string, fresh bool) error {
	name := s.name(r.Cluster, r.Namespace, r.Name)
	set, err := json.Marshal(partOf(r))
	if err != nil {
		return errors.New("the application's values cannot be written")
	}
	for range 5 {
		cur, err := s.secrets.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			sec := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.namespace, Labels: labelsOf(r)},
				Data:       map[string][]byte{keySet: set},
			}
			if token != nil && *token != "" {
				sec.Data[keyToken] = []byte(*token)
			}
			if _, err = s.secrets.Create(ctx, sec, metav1.CreateOptions{}); apierrors.IsAlreadyExists(err) {
				continue
			}
			if err != nil {
				return fmt.Errorf("saving the application's values in its Secret: %w", err)
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading the application Secret: %w", err)
		}
		next := cur.DeepCopy()
		// A Secret that still holds a record was never copied (a copy that
		// failed): the save replaces that record, as a save always did,
		// and its token stays.
		if _, uncopied := next.Data[keySpec]; (fresh && !uncopied) || next.Data == nil {
			next.Data = map[string][]byte{}
		}
		next.Data[keySet] = set
		delete(next.Data, keySpec)
		switch {
		case token != nil && *token != "":
			next.Data[keyToken] = []byte(*token)
		case token != nil:
			delete(next.Data, keyToken)
		}
		if next.Labels == nil {
			next.Labels = map[string]string{}
		}
		maps.Copy(next.Labels, labelsOf(r))
		if reflect.DeepEqual(cur.Data, next.Data) && reflect.DeepEqual(cur.Labels, next.Labels) {
			return nil
		}
		if _, err = s.secrets.Update(ctx, next, metav1.UpdateOptions{}); apierrors.IsConflict(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("saving the application's values in its Secret: %w", err)
		}
		return nil
	}
	return errors.New("the application's Secret kept changing while its values were saved")
}

// errLeftWhole says a Secret was not rewritten: it holds a record this
// console cannot read, and stays as it is.
var errLeftWhole = errors.New("the Secret's record cannot be read: it is left as it is")

// dropSpec rewrites one Secret of before the move, once its record is a
// row: "set.json" is written from the record's secret part, where the
// Secret has none yet, and "spec.json" is removed, in one update over the
// version read, so there is no moment when the values are in neither. The
// token and every other key stay. A Secret that changed meanwhile is read
// again and done over what it holds then; one with no "spec.json" is done
// already. It says whether it rewrote the Secret.
func (s *SecretStore) dropSpec(ctx context.Context, sec *corev1.Secret) (bool, error) {
	for range 5 {
		spec, has := sec.Data[keySpec]
		if !has {
			return false, nil
		}
		next := sec.DeepCopy()
		if _, set := next.Data[keySet]; !set {
			r, err := unmarshal(spec)
			if err != nil || s.name(r.Cluster, r.Namespace, r.Name) != sec.Name {
				return false, errLeftWhole
			}
			b, err := json.Marshal(partOf(r))
			if err != nil {
				return false, errLeftWhole
			}
			next.Data[keySet] = b
			if next.Labels == nil {
				next.Labels = map[string]string{}
			}
			maps.Copy(next.Labels, labelsOf(r))
		}
		delete(next.Data, keySpec)
		_, err := s.secrets.Update(ctx, next, metav1.UpdateOptions{})
		if err == nil {
			return true, nil
		}
		if !apierrors.IsConflict(err) {
			return false, fmt.Errorf("rewriting the application Secret %s: %w", sec.Name, err)
		}
		if sec, err = s.secrets.Get(ctx, sec.Name, metav1.GetOptions{}); apierrors.IsNotFound(err) {
			return false, nil
		} else if err != nil {
			return false, fmt.Errorf("reading the application Secret: %w", err)
		}
	}
	return false, fmt.Errorf("the application Secret %s kept changing while it was rewritten", sec.Name)
}
