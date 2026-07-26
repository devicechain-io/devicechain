// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package replication asserts that an instance's broker state actually matches
// the replication it was configured for (ADR-020 A0, check A).
//
// # WHY THIS IS A PACKAGE AND NOT A SCRIPT
//
// A0 exists because the HA toggle produced a 3-node NATS cluster whose every
// stream and bucket stayed single-replica: three healthy peers, three times the
// compute, zero node failures survived. The reason that shipped is that every
// check anyone ran was against RENDERED CONFIGURATION — the chart said HA, the
// tofu said HA, the pods were up. Nothing asked the broker.
//
// So the rule this package encodes is: an HA claim is asserted from OBSERVED
// BROKER STATE, never from what we asked for. Collect reads the broker; Verify is
// a pure function over what Collect saw. The split is the point:
//
//   - The impure half needs a real cluster, so it can only run in a drill.
//   - The pure half runs in CI, which is where the NEGATIVE CONTROL lives — the
//     assertion that this suite FAILS on a single-replica broker. A verifier that
//     passes on the thing it exists to catch is the exact bug A0 is about, one
//     level up, and "we ran it on a real cluster and it went green" is not
//     evidence against that.
//
// Verify therefore never reads the network, never reads a file, and never
// consults configuration. Everything it judges arrives in the Snapshot.
package replication

import (
	"fmt"
	"sort"
	"strings"
)

// Peer is one RAFT peer of a stream or consumer group, as the BROKER reports it
// — not as the configuration requested it.
//
// Current and Offline are why this type exists rather than a peer count. A stream
// reporting Replicas: 3 with a stale or offline peer is not replicated three
// ways; it is a stream that will lose data if the leader goes down, wearing the
// number that says otherwise. That is the single most likely way a merely
// "plumbed" cluster passes a naive check.
type Peer struct {
	Name    string
	Current bool
	Offline bool
}

// Object is one replicated JetStream object — a message stream, or the stream
// that backs a KV bucket — as observed on the broker.
//
// KV buckets are included as objects rather than modelled separately because at
// the broker they ARE streams (nats.go prefixes the bucket name with "KV_"), and
// the replication predicate is identical. Keeping them one type is what stops a
// future check being added to streams and quietly not to buckets — which is the
// shape that left dc_leases exposed.
type Object struct {
	// Name is the concrete broker name, e.g. "default_inbound-events" or
	// "KV_default_dc_leases".
	Name string
	// Replicas is Config.Replicas: what the broker was ASKED to maintain. It is
	// necessary and not sufficient — see Peers.
	Replicas int
	// Leader is Cluster.Leader. Empty on an unclustered broker, and empty during a
	// leader election, which is a legitimately transient state a drill has to
	// tolerate by retrying rather than by ignoring.
	Leader string
	// Peers is Cluster.Replicas, which EXCLUDES the leader. A correctly replicated
	// R3 object therefore has two entries here, not three — an easy off-by-one that
	// would make an R2 cluster look right.
	Peers []Peer
}

// Consumer is one durable consumer's replication, which is separate state from
// its stream's: a consumer group has its own RAFT group and can be less
// replicated than the stream it reads.
type Consumer struct {
	Stream string
	Name   string
	Leader string
	Peers  []Peer
}

// Pod is one broker pod and the node it was scheduled onto.
//
// Placement is part of the HA claim, not an extra. Three NATS servers co-located
// on one node with R3 streams satisfies every other assertion in this file —
// three peers, Replicas: 3, all current — and survives zero node losses. The
// upstream chart defaults topologySpreadConstraints to {}, so this is not a
// theoretical axis.
type Pod struct {
	Name string
	Node string
}

// Snapshot is everything observed, in one value. Verify judges only what is here.
type Snapshot struct {
	// Objects is every in-scope stream and KV backing stream found on the broker.
	Objects []Object
	// Consumers is every durable consumer found on those streams.
	Consumers []Consumer
	// Pods is the broker's pods and their nodes. Empty when placement was not
	// collected, which Expectation.RequirePods turns into a failure rather than a
	// skip.
	Pods []Pod
}

// Expectation is what the instance CLAIMS, expressed in concrete broker names.
//
// The names are supplied rather than derived here so this package never has to
// know how an instance id becomes a stream name. That construction lives in
// exactly one place (messaging.ReplicationExpectation), which is what stops the
// verifier looking for a bucket under a name the runtime stopped using — a drift
// whose symptom is a check that reports "missing" forever, or worse, one that
// silently has nothing to check.
type Expectation struct {
	// Replicas is the declared replica factor: 1 for a single-node instance, 3 for
	// the shipped HA topology. Verify is meaningful at 1 — that is the negative
	// control's positive half, proving the suite passes a correct R1 instance
	// rather than failing everything indiscriminately.
	Replicas int

	// Streams are the instance's message streams, required present by name.
	Streams []string
	// LeaseBucket is the KV backing stream for the ADR-070 fence substrate, checked
	// as its OWN assertion rather than as one row of a loop.
	//
	// This is deliberate duplication: LeaseBucket is also covered by the generic
	// sweep. It gets a named check because a fence bucket that silently fell back
	// to R1 is the highest-consequence single failure here — the fence is what
	// stops two owners of a partition writing over each other — and a generic loop
	// that skipped it (a name built wrong, a collection path that filtered it) would
	// report success having never looked.
	LeaseBucket string
	// StateBuckets are the other KV backing streams required present by name.
	StateBuckets []string
	// CacheBucketSuffixes are the per-area cache buckets, matched by suffix because
	// their concrete names carry a functional-area segment and therefore depend on
	// which areas are deployed. Each suffix must match at least one observed object.
	CacheBucketSuffixes []string
	// MqttStreams are the broker's own MQTT session/message streams, required
	// present by name.
	//
	// Required, not optional-if-present. Their replica factor is set once, by
	// nats-server itself, at the FIRST MQTT connect after the broker starts, and it
	// is the one replication decision the platform does not make. If no device has
	// ever connected, the streams do not exist and the lever has not been exercised
	// — reporting that as a pass would be the vacuity this package exists to
	// prevent, so a drill has to drive one MQTT connection before verifying.
	MqttStreams []string

	// Prefixes put a DISCOVERED object in scope even when nothing above named it.
	//
	// Without this the suite only ever checks what it already knew to look for, so a
	// stream added next year is replicated or not with nobody watching. With it, an
	// unexpected instance-scoped object at the wrong replica factor is a finding.
	Prefixes []string

	// RequirePods is the number of broker pods that must be observed on distinct
	// nodes. Zero disables the placement check — which Verify reports, so a skipped
	// axis is visible in the output rather than absent from it.
	RequirePods int
}

// Finding is one failed assertion, named so the output says which check failed
// and on what — the negative control requires naming the assertion that fired,
// because "it failed" is not evidence the suite examined the right thing.
type Finding struct {
	// Check is the spec label ("A1".."A5") the assertion belongs to.
	Check string
	// Object is what failed, or "" for a whole-check failure such as an empty
	// collection.
	Object string
	// Message states the defect in broker terms.
	Message string
}

func (f Finding) String() string {
	if f.Object == "" {
		return fmt.Sprintf("[%s] %s", f.Check, f.Message)
	}
	return fmt.Sprintf("[%s] %s: %s", f.Check, f.Object, f.Message)
}

// Report is the verdict.
type Report struct {
	// Replicas echoes the factor everything was judged against, so a report cannot
	// be read out of context.
	Replicas int
	// Findings is every failed assertion. Empty means the claim holds.
	Findings []Finding
	// Checked counts what was actually examined, per axis. These are printed
	// whether or not the run passed, because a green run over zero objects is the
	// failure mode that produced A0 and it is indistinguishable from a real pass
	// unless the counts are on screen.
	Checked Counts
}

// Counts is what the run actually looked at.
type Counts struct {
	Objects   int
	Streams   int
	Buckets   int
	Consumers int
	Pods      int
	Nodes     int
}

// OK reports whether every assertion held.
func (r Report) OK() bool { return len(r.Findings) == 0 }

// Verify judges a snapshot against an expectation. It is pure: same inputs, same
// findings, no I/O.
//
// The ordering of the vacuity guards matters. They run FIRST and they are
// findings, not early returns, because the question a reader has when a suite
// goes green is "did it look at anything", and that has to be answerable from the
// same output as everything else.
func Verify(snap Snapshot, exp Expectation) Report {
	rep := Report{Replicas: exp.Replicas}
	add := func(check, object, format string, args ...any) {
		rep.Findings = append(rep.Findings, Finding{
			Check: check, Object: object, Message: fmt.Sprintf(format, args...),
		})
	}

	if exp.Replicas < 1 {
		add("A0", "", "expectation declares %d replicas, which is not a topology; "+
			"nothing below was judged against a meaningful factor", exp.Replicas)
		return rep
	}

	byName := make(map[string]Object, len(snap.Objects))
	for _, o := range snap.Objects {
		byName[o.Name] = o
	}
	rep.Checked.Objects = len(snap.Objects)
	rep.Checked.Consumers = len(snap.Consumers)
	rep.Checked.Pods = len(snap.Pods)
	for _, o := range snap.Objects {
		if strings.HasPrefix(o.Name, kvStreamPrefix) {
			rep.Checked.Buckets++
		} else {
			rep.Checked.Streams++
		}
	}

	// --- vacuity guards -------------------------------------------------------
	if len(snap.Objects) == 0 {
		add("A1", "", "no JetStream objects were observed at all — every assertion "+
			"below ran over an empty set, so a pass here would mean nothing")
	}
	if len(snap.Consumers) == 0 {
		add("A3", "", "no durable consumers were observed — the consumer replication "+
			"check had nothing to judge")
	}

	// --- A1: every required object is present and replicated ------------------
	required := make([]namedRole, 0,
		len(exp.Streams)+len(exp.StateBuckets)+len(exp.MqttStreams)+1)
	for _, n := range exp.Streams {
		required = append(required, namedRole{n, "A1", "instance message stream"})
	}
	for _, n := range exp.StateBuckets {
		required = append(required, namedRole{n, "A1", "state KV bucket"})
	}
	for _, n := range exp.MqttStreams {
		required = append(required, namedRole{n, "A4", "broker MQTT stream"})
	}
	if exp.LeaseBucket != "" {
		required = append(required, namedRole{exp.LeaseBucket, "A2", "ADR-070 lease bucket"})
	}
	for _, r := range required {
		o, ok := byName[r.name]
		if !ok {
			add(r.check, r.name, "%s is MISSING from the broker; it cannot be "+
				"replicated and its absence was not treated as a pass", r.role)
			continue
		}
		if why := replicated(o, exp.Replicas); why != "" {
			add(r.check, r.name, "%s %s", r.role, why)
		}
	}

	// Cache buckets are matched by suffix: their concrete names carry a functional
	// area, so the set that exists depends on which areas are deployed. Requiring at
	// least one match per suffix keeps the check honest without hard-coding a
	// deployment shape.
	for _, suffix := range exp.CacheBucketSuffixes {
		matched := 0
		for _, o := range snap.Objects {
			if strings.HasSuffix(o.Name, suffix) {
				matched++
			}
		}
		if matched == 0 {
			add("A1", suffix, "no cache KV bucket with this suffix exists; either the "+
				"area that owns it is not deployed or it was never created, and in "+
				"neither case has its replication been demonstrated")
		}
	}

	// --- A1 (sweep): every DISCOVERED in-scope object, named or not -----------
	//
	// The named list above cannot grow itself. This is what covers a stream added
	// after this file was last edited.
	for _, o := range snap.Objects {
		if !inScope(o.Name, exp.Prefixes) {
			continue
		}
		if why := replicated(o, exp.Replicas); why != "" {
			// Suppress the duplicate when the named pass already reported it: one
			// defect, one finding, so a count of findings means something.
			if reportedByName(rep.Findings, o.Name) {
				continue
			}
			add("A1", o.Name, "in-scope object %s", why)
		}
	}

	// --- A3: durable consumers ------------------------------------------------
	for _, c := range snap.Consumers {
		if why := consumerReplicated(c, exp.Replicas); why != "" {
			add("A3", c.Stream+"/"+c.Name, "durable consumer %s", why)
		}
	}

	// --- A5: physical placement ----------------------------------------------
	if exp.RequirePods > 0 {
		nodes := map[string]string{}
		for _, p := range snap.Pods {
			nodes[p.Node] = p.Name
		}
		rep.Checked.Nodes = len(nodes)
		switch {
		case len(snap.Pods) != exp.RequirePods:
			add("A5", "", "%d broker pod(s) observed, want %d", len(snap.Pods), exp.RequirePods)
		case len(nodes) != len(snap.Pods):
			add("A5", "", "%d broker pod(s) are on only %d distinct node(s) (%s) — "+
				"replication across co-located servers survives no node loss",
				len(snap.Pods), len(nodes), strings.Join(sortedKeys(nodes), ", "))
		}
	}

	return rep
}

// namedRole is a required object plus what it is, so a missing-object finding can
// say "ADR-070 lease bucket is MISSING" instead of naming only a string.
type namedRole struct {
	name  string
	check string
	role  string
}

// kvStreamPrefix is what JetStream prepends to a KV bucket name to form its
// backing stream. Restated here rather than imported because this package is
// deliberately below messaging (so messaging's own cluster tests can use it), and
// it is only ever used to CLASSIFY an observed name for the counts — never to
// construct one. Construction stays in messaging.
const kvStreamPrefix = "KV_"

// replicated reports why an object fails the declared factor, or "" if it holds.
//
// The peer terms only apply above R1. A correct single-replica object on an
// unclustered broker has no leader and no peers, and demanding them would make
// the suite fail every healthy non-HA instance — which sounds like a stricter
// check and is actually a broken one, because a suite that fails everywhere gets
// switched off and stops being the negative control.
func replicated(o Object, want int) string {
	if o.Replicas != want {
		return fmt.Sprintf("is configured for %d replica(s), want %d", o.Replicas, want)
	}
	if want == 1 {
		return ""
	}
	if o.Leader == "" {
		return "has no RAFT leader (election in progress, or the group has lost quorum)"
	}
	if len(o.Peers) != want-1 {
		return fmt.Sprintf("reports %d peer(s) beside the leader, want %d — the "+
			"configured factor is %d but the RAFT group is not that wide",
			len(o.Peers), want-1, want)
	}
	for _, p := range o.Peers {
		if p.Offline {
			return fmt.Sprintf("has peer %q OFFLINE — the configured factor is %d but "+
				"only %d copies are live", p.Name, want, want-1)
		}
		if !p.Current {
			return fmt.Sprintf("has peer %q NOT CURRENT — it is still catching up, so "+
				"it does not yet hold a usable copy", p.Name)
		}
	}
	return ""
}

// consumerReplicated is the same predicate for a consumer group.
//
// A consumer's replicas are not configured directly — JetStream derives them from
// the stream and remaps the group when the stream's peer set changes. That is
// precisely why this is checked separately: "the code says they auto-remap" is an
// assertion about nats-server, and this is what turns it into an observation
// about our topology.
func consumerReplicated(c Consumer, want int) string {
	if want == 1 {
		return ""
	}
	if c.Leader == "" {
		return "has no RAFT leader"
	}
	if len(c.Peers) != want-1 {
		return fmt.Sprintf("reports %d peer(s) beside the leader, want %d — the "+
			"consumer group did not follow its stream to %d replicas",
			len(c.Peers), want-1, want)
	}
	for _, p := range c.Peers {
		if p.Offline {
			return fmt.Sprintf("has peer %q OFFLINE", p.Name)
		}
		if !p.Current {
			return fmt.Sprintf("has peer %q NOT CURRENT", p.Name)
		}
	}
	return ""
}

// inScope reports whether a discovered name belongs to this instance.
func inScope(name string, prefixes []string) bool {
	for _, p := range prefixes {
		if p != "" && strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// reportedByName reports whether a finding already names this object.
func reportedByName(findings []Finding, name string) bool {
	for _, f := range findings {
		if f.Object == name {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Format renders a report for a terminal: the counts first, then every finding.
//
// The counts lead deliberately. "PASS" over 0 objects and "PASS" over 42 objects
// are the same word, and only one of them means anything.
func (r Report) Format() string {
	var b strings.Builder
	fmt.Fprintf(&b, "replication check at R%d — examined %d object(s) (%d stream(s), "+
		"%d KV bucket(s)), %d durable consumer(s), %d broker pod(s) on %d node(s)\n",
		r.Replicas, r.Checked.Objects, r.Checked.Streams, r.Checked.Buckets,
		r.Checked.Consumers, r.Checked.Pods, r.Checked.Nodes)
	if r.OK() {
		b.WriteString("PASS — every object holds the declared replica factor with all peers current.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "FAIL — %d assertion(s) did not hold:\n", len(r.Findings))
	for _, f := range r.Findings {
		fmt.Fprintf(&b, "  %s\n", f)
	}
	return b.String()
}
