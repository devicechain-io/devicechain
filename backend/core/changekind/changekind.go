// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package changekind is the registry of change kinds: every kind of change a ChangeSet may
// carry declares, once and at registration, how undoable it is, whether it belongs to the
// governance plane, and which revision of its shape it speaks.
//
// This is a skeleton. It decides nothing about approval; it exists so that every later
// piece (the machine contract, the sandbox, the router) reads kinds from one place and so
// that three facts cannot be left implicit:
//
//   - The effect class. A kind that does not say whether it is reversible, compensable or
//     irreversible does not register. There is no default, because a default would be
//     the answer given to every kind nobody thought about.
//   - The governance flag. Mandates, agent policy, router tables, sampling plans, outcome
//     watches, invariants, holdout sets, kill switches and conformance checks are the
//     controls that bound an agent. They are registered here, flagged, and can never be
//     agent-writable: a kind that is both panics at registration.
//   - The version. Without one, a kind's shape could change under a stored ChangeSet.
//
// Registration happens in init or at construction, so a violation panics at startup
// rather than surfacing on a request.
package changekind

import (
	"fmt"
	"regexp"
	"sort"
	"sync"
)

// Effect is how a change can be taken back once applied. The zero value is invalid on
// purpose.
type Effect int

const (
	// EffectReversible changes can be undone exactly (the prior state is restored).
	EffectReversible Effect = iota + 1
	// EffectCompensable changes cannot be undone but a follow-up action can offset them.
	EffectCompensable
	// EffectIrreversible changes cannot be taken back or offset. They are never auto-approved.
	EffectIrreversible
)

// String returns the lower-case class name, or "invalid" for the zero or an unknown value.
func (e Effect) String() string {
	switch e {
	case EffectReversible:
		return "reversible"
	case EffectCompensable:
		return "compensable"
	case EffectIrreversible:
		return "irreversible"
	}
	return "invalid"
}

// Valid reports whether e is one of the three declared classes.
func (e Effect) Valid() bool {
	return e == EffectReversible || e == EffectCompensable || e == EffectIrreversible
}

// Kind describes one kind of change.
type Kind struct {
	// Name is the stable identifier: dotted lower-case segments, e.g. "device.profile".
	Name string
	// Effect is required.
	Effect Effect
	// Governance marks a kind that belongs to the governance plane. It can never be
	// AgentWritable.
	Governance bool
	// AgentWritable marks a kind an agent principal may include in a ChangeSet. Being
	// writable does not mean being auto-approvable; see AutoEligible.
	AgentWritable bool
	// Version is the revision of the kind's shape, starting at 1.
	Version int
}

// AutoEligible reports whether the kind could ever be applied without a human decision:
// it is not governance and its effect class is reversible or compensable. An invalid or
// zero effect class is not eligible, so a kind read from a missed lookup never is. It is a
// necessary condition only; the approval path is not part of this package.
func (k Kind) AutoEligible() bool {
	return !k.Governance && (k.Effect == EffectReversible || k.Effect == EffectCompensable)
}

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z][a-z0-9]*)*$`)

func (k Kind) validate() error {
	switch {
	case !namePattern.MatchString(k.Name):
		return fmt.Errorf("kind name %q must be dotted lower-case alphanumeric segments", k.Name)
	case !k.Effect.Valid():
		return fmt.Errorf("kind %q declares no effect class (reversible, compensable or irreversible)", k.Name)
	case k.Version < 1:
		return fmt.Errorf("kind %q declares version %d; versions start at 1", k.Name, k.Version)
	case k.Governance && k.AgentWritable:
		return fmt.Errorf("kind %q is a governance kind and cannot be agent-writable", k.Name)
	}
	return nil
}

// Reader is the read side of a registry. Default returns one so that a caller holding the
// process-wide registry can look kinds up but can neither add to it nor replace it.
type Reader interface {
	Lookup(name string) (Kind, bool)
	Kinds() []Kind
	AgentWritable() []Kind
	Governance() []Kind
}

var _ Reader = (*Registry)(nil)

// Registry holds kinds. It is safe for concurrent use.
type Registry struct {
	mu    sync.RWMutex
	kinds map[string]Kind
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{kinds: map[string]Kind{}} }

// Register adds a kind and panics if it is invalid or its name is taken. A panic is the
// point: registration runs at startup, and a malformed kind must stop the process there.
func (r *Registry) Register(k Kind) {
	if err := k.validate(); err != nil {
		panic("changekind: " + err.Error())
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.kinds[k.Name]; dup {
		panic(fmt.Sprintf("changekind: kind %q registered twice", k.Name))
	}
	r.kinds[k.Name] = k
}

// Lookup returns the kind registered under name.
func (r *Registry) Lookup(name string) (Kind, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	k, ok := r.kinds[name]
	return k, ok
}

// Kinds returns every registered kind ordered by name.
func (r *Registry) Kinds() []Kind { return r.filter(func(Kind) bool { return true }) }

// AgentWritable returns the kinds an agent principal may write, ordered by name. It never
// contains a governance kind.
func (r *Registry) AgentWritable() []Kind {
	return r.filter(func(k Kind) bool { return k.AgentWritable })
}

// Governance returns the governance-plane kinds, ordered by name.
func (r *Registry) Governance() []Kind {
	return r.filter(func(k Kind) bool { return k.Governance })
}

func (r *Registry) filter(keep func(Kind) bool) []Kind {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Kind, 0, len(r.kinds))
	for _, k := range r.kinds {
		if keep(k) {
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
