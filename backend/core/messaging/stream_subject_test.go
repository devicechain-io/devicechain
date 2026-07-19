// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"testing"

	"github.com/nats-io/nats.go"
)

// The stream's subject pattern and the publish subject are decided in different
// places — the stream at ensureStream, the subject at write time — so the only
// thing keeping them in agreement is that both derive from perDeviceSuffixes. If
// they disagree, publishes land in NO stream and command delivery stops silently.
func TestStreamSubjectCoversTheSubjectsPublished(t *testing.T) {
	const instance = "inst-1"

	t.Run("a per-device subject falls inside its stream pattern", func(t *testing.T) {
		subject := DeviceScopedSubject(instance, "acme", SubjectDeviceCommands, "sensor-001")
		pattern := StreamSubject(instance, SubjectDeviceCommands)

		if want := "inst-1.acme.device-commands.sensor-001"; subject != want {
			t.Fatalf("publish subject = %q, want %q", subject, want)
		}
		if want := "inst-1.*.device-commands.*"; pattern != want {
			t.Fatalf("stream pattern = %q, want %q", pattern, want)
		}
		if !subjectMatches(pattern, subject) {
			t.Fatalf("stream %q does not capture published subject %q", pattern, subject)
		}
	})

	t.Run("a tenant-scoped subject falls inside its stream pattern", func(t *testing.T) {
		subject := ScopedSubject(instance, "acme", SubjectCommandResponses)
		pattern := StreamSubject(instance, SubjectCommandResponses)

		if !subjectMatches(pattern, subject) {
			t.Fatalf("stream %q does not capture published subject %q", pattern, subject)
		}
	})

	// The whole point of the per-device shape: one device's subject must not be
	// reachable by a filter scoped to another device.
	t.Run("one device's subject is outside another device's filter", func(t *testing.T) {
		mine := DeviceScopedSubject(instance, "acme", SubjectDeviceCommands, "sensor-001")
		theirFilter := DeviceScopedSubject(instance, "acme", SubjectDeviceCommands, "sensor-002")

		if subjectMatches(theirFilter, mine) {
			t.Fatal("a device-scoped filter must not match another device's subject")
		}
	})

	// The old tenant-scoped shape must NOT still match, or the migration would be
	// cosmetic: a device granted the old subject would keep receiving everything.
	t.Run("the old tenant-wide pattern no longer captures commands", func(t *testing.T) {
		subject := DeviceScopedSubject(instance, "acme", SubjectDeviceCommands, "sensor-001")
		old := WildcardSubject(instance, SubjectDeviceCommands)

		if subjectMatches(old, subject) {
			t.Fatalf("old pattern %q still captures %q; the subject did not actually change shape",
				old, subject)
		}
	})
}

// applyStreamSubjects is what lets an EXISTING cluster survive a subject-shape
// change. Without it a fresh install works and every upgraded cluster silently
// stops delivering commands, which is the asymmetry that makes this class of bug
// so hard to see: the green install proves nothing about the upgrade.
func TestApplyStreamSubjects(t *testing.T) {
	t.Run("rewrites a stream created with the old shape", func(t *testing.T) {
		cfg := &nats.StreamConfig{Subjects: []string{"inst-1.*.device-commands"}}
		want := []string{"inst-1.*.device-commands.*"}

		if !applyStreamSubjects(cfg, want) {
			t.Fatal("a differing subject list must report a change")
		}
		if len(cfg.Subjects) != 1 || cfg.Subjects[0] != want[0] {
			t.Fatalf("subjects = %v, want %v", cfg.Subjects, want)
		}
	})

	// A full replacement, not a union. Keeping the old subject would leave the
	// tenant-wide command subject alive — the exact thing this change removes.
	t.Run("replaces rather than accumulates", func(t *testing.T) {
		cfg := &nats.StreamConfig{Subjects: []string{"inst-1.*.device-commands"}}
		applyStreamSubjects(cfg, []string{"inst-1.*.device-commands.*"})

		for _, s := range cfg.Subjects {
			if s == "inst-1.*.device-commands" {
				t.Fatal("the old tenant-wide subject survived the reconcile")
			}
		}
	})

	// Every other stream in the platform computes the subject list it already has,
	// so reconciliation must be a no-op for them — otherwise every service restart
	// would issue a pointless UpdateStream against every stream.
	t.Run("is a no-op when the shape is unchanged", func(t *testing.T) {
		cfg := &nats.StreamConfig{Subjects: []string{"inst-1.*.inbound-events"}}
		if applyStreamSubjects(cfg, []string{"inst-1.*.inbound-events"}) {
			t.Fatal("an identical subject list must not report a change")
		}
	})
}

// subjectMatches implements NATS subject matching for the shapes used here:
// "*" matches exactly one token, ">" matches one or more trailing tokens.
func subjectMatches(pattern, subject string) bool {
	p := splitTokens(pattern)
	s := splitTokens(subject)
	for i, tok := range p {
		if tok == ">" {
			return i < len(s)
		}
		if i >= len(s) {
			return false
		}
		if tok != "*" && tok != s[i] {
			return false
		}
	}
	return len(p) == len(s)
}

func splitTokens(subject string) []string {
	out := []string{}
	cur := ""
	for _, r := range subject {
		if r == '.' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	return append(out, cur)
}

// An undeclared suffix must not produce a stream. JetStream reserves MaxBytes UP
// FRONT, so a stream outside core/streams reserves disk the budget never counted
// — and the budget is what sizes the PV. Before this guard, the only thing
// keeping the declaration complete was that someone remembered to update a list.
func TestEnsureStreamRefusesUndeclaredSuffix(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	if _, err := nmgr.NewWriter("a-suffix-nobody-declared"); err == nil {
		t.Fatal("creating a stream for an undeclared suffix must fail; " +
			"an unbudgeted stream is exactly what crashloops a fresh bring-up")
	}
	// A declared one still works — the guard must not be a blanket refusal.
	if _, err := nmgr.NewWriter(SubjectDeviceCommands); err != nil {
		t.Fatalf("a declared suffix must still create its stream: %v", err)
	}
}
