// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/kv"
	"github.com/devicechain-io/dc-microservice/streams"
)

// WHO MAY SHAPE THE BROKER.
//
// A stream's configuration is more than bookkeeping. A login that can create or update a stream
// can give it any subjects (capturing another login's inbox, another bucket's keys, the broker's
// own authorization requests, and reading them back from its own stream), can RePublish or
// subject-transform what is stored into a subject someone else trusts, and can source from or
// mirror another stream. A grant that names the stream restricts only the name. So the right to
// create or update a stream or a bucket is held by ONE login, the broker administrator, and the
// platform's services only look:
//
//   - the administrator (NewBrokerAdmin) runs ReconcileBroker, once per install and upgrade, from
//     the chart's pre-install/pre-upgrade hook Job (and on demand with `dcctl broker reconcile`).
//     It runs the code that every service used to run at its own start: ensureStream, the
//     bucket reconcile, the retention migration;
//   - every other manager over a deployed microservice VERIFIES what it opens (verifyStream,
//     verifyKvBucket) and refuses to go on when it does not match what core/streams and core/kv
//     declare, with an error that names what differs and what to run.
//
// A manager that is not over a deployed microservice (a test's) still reconciles what it opens,
// so the many tests that build a stream by opening a writer on it are not each a two-step.

// ReconcileAction is what the administrator did to one stream or bucket.
type ReconcileAction struct {
	Kind   string // "stream" or "bucket"
	Name   string
	Action string // created, updated, unchanged, present, migrated
}

// ReconcileReport is everything one ReconcileBroker pass did.
type ReconcileReport struct {
	Actions []ReconcileAction
}

// Count returns how many actions had the given action.
func (r *ReconcileReport) Count(action string) int {
	n := 0
	for _, a := range r.Actions {
		if a.Action == action {
			n++
		}
	}
	return n
}

// NewBrokerAdmin returns the manager of the broker administrator. ms carries the instance
// configuration (the broker address, the administrator's login and the stream bounds) and the
// instance id; it is never a deployed microservice, so the catalog's per-area checks do not
// apply to it, and it is the one manager allowed to create and change streams and buckets.
//
// The caller connects it with ExecuteInitialize and closes it with ExecuteStop; there is no
// lifecycle behind it, because it serves nothing.
func NewBrokerAdmin(ms *core.Microservice) *NatsManager {
	ms.FunctionalArea = config.NatsAdminPrincipal
	nmgr := NewNatsManager(ms, core.LifecycleCallbacks{}, nil)
	nmgr.admin = true
	return nmgr
}

// Close ends the administrator's connection as an orderly close, which the liveness handlers do
// not mistake for a death.
func (nmgr *NatsManager) Close() { nmgr.closeConn() }

// mayReconcile reports whether this manager creates and reconfigures what it opens: the
// administrator does, and so does a manager that is not held to the catalog (a test's).
func (nmgr *NatsManager) mayReconcile() bool { return nmgr.admin || !nmgr.enforced() }

// mayReconcileBucket is mayReconcile for the bucket with this logical name. The identity account
// is the one exception on a deployed manager: it is a boundary of its own, entered by the login
// that user-management alone holds, so that login creates the sign-in buckets itself and the
// administrator never enters the account.
func (nmgr *NatsManager) mayReconcileBucket(logical string) bool {
	return nmgr.mayReconcile() || (kv.AccountFor(logical) == kv.Identity && nmgr.identityRequired())
}

func (nmgr *NatsManager) noteReconciled(kind, name, action string) {
	if nmgr.reconciled != nil {
		nmgr.reconciled(kind, name, action)
	}
}

// remedy is the sentence every drift error ends with.
const remedy = "Services only verify the broker's streams and buckets; the broker administrator " +
	"creates and changes them, in the chart's pre-install/pre-upgrade hook (the dc-broker-admin Job). " +
	"Run `dcctl broker reconcile` (or `helm upgrade`, which runs the hook), and this pod starts once it matches"

// BrokerDriftError is the named error a service stops with when a stream or bucket it opens is
// missing or does not match what this build declares.
type BrokerDriftError struct {
	Kind     string // "stream" or "KV bucket"
	Name     string
	Problems []string
}

func (e *BrokerDriftError) Error() string {
	return fmt.Sprintf("messaging: JetStream %s %q does not match what this build declares: %s. %s",
		e.Kind, e.Name, strings.Join(e.Problems, "; "), remedy)
}

// streamExpectation is what a stream must be, for a service that verifies it. It is the part of
// a stream's configuration that decides what is stored under which subject, and so what a reader
// may trust. Ceilings and replicas are not in it: they are sizing, the administrator reconciles
// them, and a service that stopped over a ceiling would turn a tuning change into an outage.
type streamExpectation struct {
	subjects    []string
	retention   nats.RetentionPolicy
	duplicates  time.Duration
	allowDirect bool
}

func (nmgr *NatsManager) expectedStream(suffix string) streamExpectation {
	return streamExpectation{
		subjects:    StreamSubjects(nmgr.Microservice.InstanceId, suffix),
		retention:   retentionPolicy(streams.RetentionFor(suffix)),
		duplicates:  time.Duration(streams.DuplicateWindowSecondsFor(suffix)) * time.Second,
		allowDirect: streams.AllowDirectFor(suffix),
	}
}

// streamDrift lists how cfg differs from w. Each entry reads as a sentence fragment.
func streamDrift(cfg nats.StreamConfig, w streamExpectation) []string {
	var out []string
	if !slices.Equal(cfg.Subjects, w.subjects) {
		out = append(out, fmt.Sprintf("it captures subjects %v, not the declared %v", cfg.Subjects, w.subjects))
	}
	if cfg.Retention != w.retention {
		out = append(out, fmt.Sprintf("its retention is %s, not the declared %s", cfg.Retention, w.retention))
	}
	if cfg.Discard != nats.DiscardOld || cfg.DiscardNewPerSubject {
		out = append(out, "it is set to discard new messages when full, not the declared discard-old")
	}
	if w.duplicates > 0 && cfg.Duplicates != w.duplicates {
		out = append(out, fmt.Sprintf("its duplicate window is %s, not the declared %s", cfg.Duplicates, w.duplicates))
	}
	if w.allowDirect && !cfg.AllowDirect {
		out = append(out, "it does not allow direct gets, which the replay and preview reads need")
	}
	// A stream that stores under a subject it does not capture is how a message is forged into
	// another stream's namespace. No declared stream has any of these, so any is drift.
	if cfg.Mirror != nil {
		out = append(out, "it is a mirror")
	}
	if len(cfg.Sources) > 0 {
		out = append(out, "it has sources")
	}
	if cfg.RePublish != nil {
		out = append(out, "it republishes what it stores")
	}
	if cfg.SubjectTransform != nil {
		out = append(out, "it transforms the subjects it stores")
	}
	return out
}

// verifyStream is ensureStream for a manager that may not change the broker: the stream must
// already exist, as declared.
func (nmgr *NatsManager) verifyStream(suffix string) (string, error) {
	name := StreamName(nmgr.Microservice.InstanceId, suffix)
	want := nmgr.expectedStream(suffix)
	var drift *BrokerDriftError
	err := core.RetryInfraConnect(nmgr.opContext(), "nats jetstream", func(ctx context.Context) error {
		info, err := nmgr.js.StreamInfo(name, nats.Context(ctx))
		switch {
		case errors.Is(err, nats.ErrStreamNotFound):
			drift = &BrokerDriftError{Kind: "stream", Name: name, Problems: []string{"it does not exist"}}
			return nil
		case err != nil:
			return err
		}
		if p := streamDrift(info.Config, want); len(p) > 0 {
			drift = &BrokerDriftError{Kind: "stream", Name: name, Problems: p}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if drift != nil {
		return "", drift
	}
	nmgr.trackStream(name, suffix)
	return name, nil
}

// verifyKvBucket is the bucket half of verifyStream: the bucket must exist, hold keys under its
// own subjects and nothing else, and not be a mirror, source or republisher.
func (nmgr *NatsManager) verifyKvBucket(js nats.JetStreamContext, logical, bucket string, tracked func(string)) (nats.KeyValue, error) {
	stream := kvStreamPrefix + bucket
	var drift *BrokerDriftError
	var existing nats.KeyValue
	err := core.RetryInfraConnect(nmgr.opContext(), "nats jetstream", func(ctx context.Context) error {
		info, err := js.StreamInfo(stream, nats.Context(ctx))
		switch {
		case errors.Is(err, nats.ErrStreamNotFound):
			drift = &BrokerDriftError{Kind: "KV bucket", Name: bucket, Problems: []string{"it does not exist"}}
			return nil
		case err != nil:
			return err
		}
		var p []string
		if want := []string{"$KV." + bucket + ".>"}; !slices.Equal(info.Config.Subjects, want) {
			p = append(p, fmt.Sprintf("it captures subjects %v, not %v", info.Config.Subjects, want))
		}
		if info.Config.Mirror != nil || len(info.Config.Sources) > 0 {
			p = append(p, "it is a mirror or has sources")
		}
		if info.Config.RePublish != nil || info.Config.SubjectTransform != nil {
			p = append(p, "it republishes or transforms what it stores")
		}
		if len(p) > 0 {
			drift = &BrokerDriftError{Kind: "KV bucket", Name: bucket, Problems: p}
			return nil
		}
		existing, err = js.KeyValue(bucket)
		return err
	})
	if err != nil {
		return nil, err
	}
	if drift != nil {
		return nil, drift
	}
	tracked(bucket)
	return existing, nil
}

// ReconcileBroker creates and reconciles every stream and bucket the given areas open, in the
// shared account: the buckets first (the retention migration takes a lock in one of them), then
// the streams, then the declared retention migration. It is idempotent and safe to repeat, and it
// leaves what it did in the report even when it fails part of the way.
//
// areas are the functional areas the installation deploys. A stream is reconciled when a deployed
// area opens it (writes it or consumes it), which is the set the services' own starts used to
// create; a stream nobody deployed opens is not created, so an installation with fewer areas has
// fewer streams, as before.
func (nmgr *NatsManager) ReconcileBroker(ctx context.Context, areas []string) (*ReconcileReport, error) {
	// The report is never nil, refusals included: a caller prints what was done before it looks
	// at the error, and an empty report is what "nothing was done" reads as.
	report := &ReconcileReport{}
	if !nmgr.admin {
		return report, errors.New("messaging: only the broker administrator's manager reconciles the broker")
	}
	for _, a := range areas {
		if !streams.IsPlatformArea(a) {
			return report, fmt.Errorf("messaging: %q is not a platform functional area (known: %v)", a, streams.PlatformAreas)
		}
	}
	nmgr.reconciled = func(kind, name, action string) {
		report.Actions = append(report.Actions, ReconcileAction{Kind: kind, Name: name, Action: action})
	}
	defer func() { nmgr.reconciled = nil }()
	nmgr.setRunContext(ctx)
	defer nmgr.endRunContext()

	opened := func(openers []string) bool {
		for _, a := range openers {
			if slices.Contains(areas, a) {
				return true
			}
		}
		return false
	}

	seen := map[string]bool{}
	for _, b := range kv.All {
		if !opened(b.Admins()) {
			continue
		}
		var names []string
		switch {
		case b.Account == kv.App:
			for _, a := range b.Admins() {
				if slices.Contains(areas, a) {
					names = append(names, KvBucketNames(nmgr.Microservice.InstanceId, b, a)...)
				}
			}
		case !nmgr.identityRequired():
			// The identity account does not exist on this broker (an older one), so user-management
			// keeps its sign-in buckets in the shared account, as it always did, and they are
			// created here with the rest. Where the account exists its own login creates them.
			names = append(names, identityBucketName(nmgr.Microservice.InstanceId, b.Name))
		}
		for _, bucket := range names {
			if seen[bucket] {
				continue
			}
			seen[bucket] = true
			if _, err := nmgr.KeyValueStore(b.Name, bucket, b.TTL); err != nil {
				return report, fmt.Errorf("reconciling KV bucket %s: %w", bucket, err)
			}
		}
	}

	for _, st := range streams.All {
		if st.Dormant || !opened(streams.AdminsOf(st.Suffix)) {
			continue
		}
		if _, err := nmgr.ensureStream(st.Suffix); err != nil {
			return report, fmt.Errorf("reconciling stream %s: %w", StreamName(nmgr.Microservice.InstanceId, st.Suffix), err)
		}
	}

	if err := nmgr.MigratePendingRetention(ctx); err != nil {
		return report, err
	}
	log.Info().Int("created", report.Count("created")).Int("updated", report.Count("updated")).
		Int("unchanged", report.Count("unchanged")+report.Count("present")).
		Msg("Reconciled the broker's streams and buckets")
	return report, nil
}
