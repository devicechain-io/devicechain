// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"errors"
	"fmt"
	"strings"

	sppb "github.com/devicechain-io/dc-sparkplug-ingest/proto"
)

// The BIRTH metric table (G19 S2).
//
// A Sparkplug command is only meaningful against what the node DECLARED at birth: the
// datatype a value must be encoded into, and the alias (if the node aliased the metric) the
// command must be addressed by. The session machine already holds the alias -> name map for
// validating DATA; this adds the inverse, name -> (alias, datatype), per node and per device.
//
// Life cycle (all under SessionTracker.mu, so a lookup sees a table exactly as one birth left it):
//
//   - rebuilt from scratch on an accepted NBIRTH, and on the NBIRTH that answers a commanded
//     rebirth (the same session, but the node may have remapped every alias);
//   - extended by each DBIRTH, which replaces that device's entries;
//   - a device's entries dropped on its DDEATH;
//   - cleared on NDEATH, and for every node at the end of the Host's broker session.
//
// Every one of those moves birthGen. A command captures (sessionEpoch, bdSeq, birthGen) with its
// alias and checks them again immediately before it publishes, so an alias captured before a
// rebirth is never used after it.

// metricKey scopes a metric name by device: "" is the edge node's own metrics.
type metricKey struct {
	device string
	name   string
}

// declaredMetric is what a BIRTH declared for one metric.
type declaredMetric struct {
	alias    uint64
	hasAlias bool
	datatype uint32
}

// CommandTarget is the Sparkplug identity a command is addressed to: a node (Device empty,
// NCMD) or a device under a node (DCMD). The identity decides the verb, not the command key.
type CommandTarget struct {
	Group  string
	Node   string
	Device string
}

// IsDevice reports whether the target is a device (DCMD) rather than a node (NCMD).
func (t CommandTarget) IsDevice() bool { return t.Device != "" }

// Verb is the command message type for the target.
func (t CommandTarget) Verb() MessageType {
	if t.IsDevice() {
		return DCMD
	}
	return NCMD
}

// Topic is the command topic: [tck-id-topics-ncmd-topic] spBv1.0/{group}/NCMD/{node} for a node,
// [tck-id-topics-dcmd-topic] spBv1.0/{group}/DCMD/{node}/{device} for a device.
func (t CommandTarget) Topic() string {
	base := Namespace + "/" + t.Group + "/" + string(t.Verb()) + "/" + t.Node
	if t.IsDevice() {
		return base + "/" + t.Device
	}
	return base
}

func (t CommandTarget) String() string {
	if t.IsDevice() {
		return t.Group + "/" + t.Node + "/" + t.Device
	}
	return t.Group + "/" + t.Node
}

// MetricRef is a metric resolved against the BIRTH table, with the session facts it was
// resolved under. It is what validation captures and what the publish re-checks.
type MetricRef struct {
	Target   CommandTarget
	Name     string
	Alias    uint64
	HasAlias bool
	Datatype uint32

	// Epoch, BdSeq and Gen identify the table the ref was read from: the node session's epoch,
	// its birth's bdSeq, and the table's generation (see nodeSession.birthGen).
	Epoch    uint64
	BdSeq    uint64
	Gen      uint64
	HasBdSeq bool
}

// Errors a lookup or re-check answers. They are distinct on purpose: S3 maps "not born" to PARK
// (a later birth can satisfy it) and the others to a refusal with a reason.
var (
	// ErrNotBorn: the node (or the device under it) is not born on this Host session. There is no
	// table to check against, so nothing can be addressed or encoded yet.
	ErrNotBorn = errors.New("target is not born on this Host session")
	// ErrMetricUnknown: the target is born but its BIRTH declared no metric of that name.
	ErrMetricUnknown = errors.New("metric is not declared in the target's BIRTH")
	// ErrDatatypeChanged: a rebirth re-declared the metric with a different datatype than the one
	// the command was validated against.
	ErrDatatypeChanged = errors.New("metric datatype changed in a rebirth")
)

// LookupMetric resolves name against the target's BIRTH table.
func (tr *SessionTracker) LookupMetric(target CommandTarget, name string) (MetricRef, error) {
	tr.mu.Lock()
	ref, err := tr.lookupLocked(target, name)
	askRebirth := false
	if s := tr.sessions[nodeKey{target.Group, target.Node}]; s != nil && s.online && s.invalidated {
		// The table was dropped when the Host session ended. The node never died, so nothing will
		// re-declare it unprompted (a secondary Host is not bound to ask for one): ask, once per
		// backoff window. The commanded-rebirth NBIRTH rebuilds the table.
		askRebirth = tr.wantRebirth(s)
	}
	tr.mu.Unlock()
	if askRebirth && tr.rebirth != nil {
		tr.rebirth(target.Group, target.Node)
	}
	return ref, err
}

// RevalidateMetric is the check made immediately before a publish. If the table the ref was
// read from is still the current one (same session epoch, same generation), the ref is returned
// as it is. Otherwise a rebirth (or a new session) has happened in between: the metric is
// resolved once more BY NAME from the new table, so the command goes out with the alias the node
// holds now; if the metric has gone, or its datatype is no longer the one validated, the command
// is refused rather than sent on a stale alias.
func (tr *SessionTracker) RevalidateMetric(ref MetricRef) (MetricRef, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	s := tr.sessions[nodeKey{ref.Target.Group, ref.Target.Node}]
	if s != nil && s.online && s.sessionEpoch == ref.Epoch && s.birthGen == ref.Gen {
		return ref, nil
	}
	cur, err := tr.lookupLocked(ref.Target, ref.Name)
	if err != nil {
		return MetricRef{}, err
	}
	if cur.Datatype != ref.Datatype {
		return MetricRef{}, fmt.Errorf("%w: %s was datatype %d when validated, is %d now", ErrDatatypeChanged, ref.Name, ref.Datatype, cur.Datatype)
	}
	return cur, nil
}

// EndHostSession forgets every node's metric table: the Host's broker session has ended, so a
// table is no longer something a command may be addressed against. The sessions themselves are
// kept (their rebirth bookkeeping outlives a reconnect), but each live one is marked INVALIDATED,
// so a lookup answers ErrNotBorn (park, and wait for the rebuild) rather than ErrMetricUnknown
// (refuse). The next lookup asks the node to rebirth; its answer rebuilds the table.
func (tr *SessionTracker) EndHostSession() {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	for _, s := range tr.sessions {
		tr.clearMetrics(s)
		s.invalidated = s.online
	}
}

func (tr *SessionTracker) lookupLocked(target CommandTarget, name string) (MetricRef, error) {
	s := tr.sessions[nodeKey{target.Group, target.Node}]
	if s == nil || !s.online || s.invalidated {
		return MetricRef{}, fmt.Errorf("%w: %s", ErrNotBorn, target)
	}
	if target.IsDevice() {
		// Born AND declared since the last NBIRTH: after a rebirth the device is still in the born
		// set, but its metrics are not known again until its own DBIRTH.
		if _, born := s.devices[target.Device]; !born || !s.declared[target.Device] {
			return MetricRef{}, fmt.Errorf("%w: %s", ErrNotBorn, target)
		}
	}
	m, ok := s.metrics[metricKey{target.Device, name}]
	if !ok {
		return MetricRef{}, fmt.Errorf("%w: %q on %s", ErrMetricUnknown, name, target)
	}
	return MetricRef{
		Target: target, Name: name, Alias: m.alias, HasAlias: m.hasAlias, Datatype: m.datatype,
		Epoch: s.sessionEpoch, BdSeq: s.bdSeq, HasBdSeq: s.hasBdSeq, Gen: s.birthGen,
	}, nil
}

// bump moves the table's generation. The caller holds tr.mu.
func (tr *SessionTracker) bump(s *nodeSession) {
	tr.tableGen++
	s.birthGen = tr.tableGen
}

// rebuildNodeMetrics replaces the whole table with the node metrics of an NBIRTH. Devices'
// entries are dropped with it: the rebirth that follows re-declares them in their DBIRTHs, and
// until it does their aliases are exactly what must not be trusted.
func (tr *SessionTracker) rebuildNodeMetrics(s *nodeSession, p *sppb.Payload) {
	s.metrics = map[metricKey]declaredMetric{}
	s.declared = map[string]bool{}
	s.invalidated = false
	addBirthMetrics(s.metrics, "", p)
	tr.bump(s)
}

// recordDeviceMetrics replaces one device's entries with those of its DBIRTH.
func (tr *SessionTracker) recordDeviceMetrics(s *nodeSession, device string, p *sppb.Payload) {
	if s.metrics == nil {
		s.metrics = map[metricKey]declaredMetric{}
	}
	for k := range s.metrics {
		if k.device == device {
			delete(s.metrics, k)
		}
	}
	if s.declared == nil {
		s.declared = map[string]bool{}
	}
	s.declared[device] = true
	addBirthMetrics(s.metrics, device, p)
	tr.bump(s)
}

// dropDeviceMetrics removes one device's entries (its DDEATH).
func (tr *SessionTracker) dropDeviceMetrics(s *nodeSession, device string) {
	delete(s.declared, device)
	for k := range s.metrics {
		if k.device == device {
			delete(s.metrics, k)
		}
	}
	tr.bump(s)
}

// clearMetrics empties the table (NDEATH, end of the Host session).
func (tr *SessionTracker) clearMetrics(s *nodeSession) {
	s.metrics = nil
	s.declared = nil
	tr.bump(s)
}

// addBirthMetrics records every named metric a BIRTH declares. A metric with no name cannot be
// addressed by a command and is skipped (a BIRTH must name every metric,
// [tck-id-payloads-alias-birth-requirement]). The reserved "Node Control/*" metrics are kept: the
// table records what was declared, and which of them a command may target is S3's gate.
func addBirthMetrics(table map[metricKey]declaredMetric, device string, p *sppb.Payload) {
	for _, m := range p.GetMetrics() {
		name := m.GetName()
		if name == "" || strings.TrimSpace(name) == "" {
			continue
		}
		table[metricKey{device, name}] = declaredMetric{
			alias: m.GetAlias(), hasAlias: m.Alias != nil, datatype: m.GetDatatype(),
		}
	}
}
