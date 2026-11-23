// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tofurun

import (
	"fmt"
	"sort"
	"strings"
)

// Class is how a change is judged. Unclassified is not a class: it is a refusal.
type Class string

// Gate classes, in increasing order of concern.
const (
	ClassSafe        Class = "safe"
	ClassDestructive Class = "destructive"
	ClassForbidden   Class = "forbidden"
)

// Verdict is the gate's answer for one plan and one kind.
type Verdict struct {
	Allowed bool
	Worst   Class
	// Reasons name each refusal by address; they carry no value.
	Reasons []string
}

// Gate is the runner's plan gate. It is deliberately smaller than the manager's versioned table:
// it knows actions, an allow-list of resource types and the per-kind rule, and refuses everything
// else. The manager's own gate is the authority over approval; this one stops the runner applying
// a plan the rules refuse even if the manager were wrong or absent.
//
// AllowedTypes is the set of resource types the roots may change. A changing resource of a type
// outside it is forbidden in every kind, destroy included, and a nil or empty set refuses every
// change: fail closed.
type Gate struct {
	AllowedTypes map[string]bool
}

func classOfActions(actions []string) (Class, bool) {
	if len(actions) == 1 {
		switch actions[0] {
		case "no-op", "read", "create", "update":
			return ClassSafe, true
		case "delete", "forget":
			return ClassDestructive, true
		}
		return ClassForbidden, false
	}
	if len(actions) == 2 && ((actions[0] == "delete" && actions[1] == "create") || (actions[0] == "create" && actions[1] == "delete")) {
		return ClassDestructive, true // a replacement deletes
	}
	return ClassForbidden, false
}

func isInert(actions []string) bool {
	return len(actions) == 1 && (actions[0] == "no-op" || actions[0] == "read")
}

func isPureDelete(actions []string) bool {
	return len(actions) == 1 && actions[0] == "delete"
}

// protectedPaths are attribute names only the teardown protocol's UNPROTECT and REPROTECT runs may
// change on an existing resource: flipping deletion_protection is what makes a cluster deletable.
var protectedPaths = []string{"deletion_protection"}

func touchesProtected(f Fact) bool {
	if len(f.Actions) == 1 && f.Actions[0] == "create" {
		return false // a new resource is born with its protection; that is not a change of it
	}
	for _, p := range f.ChangedPaths {
		for _, pp := range protectedPaths {
			if p == pp || strings.HasSuffix(p, "."+pp) {
				return true
			}
		}
	}
	return false
}

func onlyProtected(f Fact) bool {
	if len(f.ChangedPaths) == 0 {
		return false
	}
	for _, p := range f.ChangedPaths {
		ok := false
		for _, pp := range protectedPaths {
			ok = ok || p == pp || strings.HasSuffix(p, "."+pp)
		}
		if !ok {
			return false
		}
	}
	return true
}

// Judge applies the rules for kind to facts:
//
//   - CREATE, CHANGE, UPGRADE: create and update only. Any delete or replacement is refused.
//   - DRIFT_CHECK, RECOVER: refresh-only. Any change, and any drift on a RECOVER, is refused.
//   - DESTROY: pure deletes only; an update or create in a destroy plan is refused.
//   - UNPROTECT, REPROTECT: updates that change deletion_protection and nothing else.
//   - every other kind: a change to deletion_protection is forbidden (the per-kind forbidden path).
//   - any kind: a refusal fact, an unlisted type or an unknown action set is forbidden.
func (g Gate) Judge(kind Kind, p PlanFacts) Verdict {
	v := Verdict{Allowed: true, Worst: ClassSafe}
	deny := func(c Class, format string, args ...any) {
		v.Allowed = false
		if c == ClassForbidden || (c == ClassDestructive && v.Worst == ClassSafe) {
			v.Worst = c
		}
		v.Reasons = append(v.Reasons, fmt.Sprintf(format, args...))
	}
	if _, err := ClassFor(kind); err != nil {
		deny(ClassForbidden, "unknown run kind")
		return v
	}
	if p.ChangeCount != len(p.Facts) {
		deny(ClassForbidden, "resource_change_count %d does not equal the %d facts", p.ChangeCount, len(p.Facts))
	}
	for _, f := range p.Facts {
		switch {
		case f.Refusal != "":
			deny(ClassForbidden, "%s: %s", f.Address, f.Refusal)
			continue
		case !isInert(f.Actions) && !g.AllowedTypes[f.Type]:
			// An inert fact (no-op, read) changes nothing; a changing one of a type nobody listed
			// is an address outside the owned set.
			deny(ClassForbidden, "%s: type %q is not an owned resource type", f.Address, f.Type)
			continue
		}
		c, known := classOfActions(f.Actions)
		if !known {
			deny(ClassForbidden, "%s: unclassified action set %v", f.Address, f.Actions)
			continue
		}
		if isInert(f.Actions) {
			continue
		}
		if kind != KindUnprotect && kind != KindReprotect && touchesProtected(f) {
			deny(ClassForbidden, "%s: changes deletion_protection, which only an UNPROTECT or REPROTECT run may", f.Address)
			continue
		}
		switch kind {
		case KindUnprotect, KindReprotect:
			if !(len(f.Actions) == 1 && f.Actions[0] == "update" && onlyProtected(f)) {
				deny(ClassForbidden, "%s: %v is not a change of deletion_protection alone", f.Address, f.Actions)
			}
		case KindCreate, KindChange, KindUpgrade:
			if c != ClassSafe {
				deny(ClassDestructive, "%s: %v is refused in a %s run", f.Address, f.Actions, kind)
			}
		case KindDriftCheck, KindRecover:
			deny(ClassDestructive, "%s: %v: a %s run may change nothing", f.Address, f.Actions, kind)
		case KindDestroy:
			if !isPureDelete(f.Actions) {
				deny(ClassForbidden, "%s: %v is not a delete: a destroy run may only delete", f.Address, f.Actions)
			}
		}
	}
	if kind == KindRecover && p.DriftCount > 0 {
		deny(ClassDestructive, "the refresh found %d drifted resource(s): a RECOVER run proceeds only on a plan with none", p.DriftCount)
	}
	sort.Strings(v.Reasons)
	return v
}
