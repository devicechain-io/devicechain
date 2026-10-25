// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"fmt"
	"slices"

	"github.com/devicechain-io/dc-microservice/kv"
	"github.com/devicechain-io/dc-microservice/streams"
)

// This file is where a service's use of the broker is held to what core/streams and core/kv
// declare for its area. The declarations say, per stream and per bucket, which areas write,
// which read (and in which mode, so under which consumer name) and which administer; the
// checks below are the only code that asks, and every way a service touches a stream or a
// bucket passes through one of them BEFORE it touches the broker.
//
// 🔴 THIS IS THE DISTINCTION, NOT A GUARD OVER IT. The alternative that was weighed first was a
// test that scans service source for stream names and compares them with the catalog. That
// keeps two descriptions of one fact and a gate to reconcile them, and it sees only the call
// shapes it was taught. Here the only way to obtain a writer, a reader, a replay, a live
// feed or a bucket is a NatsManager method, the method names the area from its own identity,
// and the catalog is the only thing it consults: a service cannot use what its area does not
// declare, and the first start that tries fails with the stream, the area and the missing role.
// (Typed handles, where `streams.InboundEvents` becomes a value that can only be turned into a
// writer if the area was declared, would move the refusal from start to compile time. It was
// not done because it changes the argument of ~80 call sites and every test fixture for a
// failure that already stops the pod before it serves, and because the checks here are what a
// broker-side grant derives from later, so they must exist either way.)
//
// WHAT IT DOES NOT SEE: a raw connection (NatsManager.Conn) bypasses it. The named users are
// the tenant purge (user-management purges every stream and bucket by subject), the callout
// responder and the session-kick requester, none of which opens a stream through this
// package. The repository guard in core/test (catalog_usage_repo_test.go) holds the call
// sites that DO go through it to the catalog from the other side: a declared role no code
// uses is a grant to nothing.

// enforced reports whether this manager's area is held to the catalog: every manager over a
// deployed microservice is, and a test fixture's is not (see core.Microservice.Deployed).
func (nmgr *NatsManager) enforced() bool {
	return nmgr.Microservice != nil && nmgr.Microservice.Deployed
}

// area is the functional area the catalog is asked about.
func (nmgr *NatsManager) area() string { return nmgr.Microservice.FunctionalArea }

// requireWriter refuses a writer on suffix unless the area is declared to write it.
func (nmgr *NatsManager) requireWriter(suffix string) error {
	if !nmgr.enforced() || streams.Writes(suffix, nmgr.area()) {
		return nil
	}
	return nmgr.undeclared("write", suffix, streams.WritersOf(suffix))
}

// requireAdmin refuses to create or reconcile suffix's stream unless the area administers it.
func (nmgr *NatsManager) requireAdmin(suffix string) error {
	if !nmgr.enforced() || streams.Administers(suffix, nmgr.area()) {
		return nil
	}
	return nmgr.undeclared("administer", suffix, streams.AdminsOf(suffix))
}

// requireReader refuses a read of suffix in mode unless the area declares it.
func (nmgr *NatsManager) requireReader(suffix string, mode streams.ReadMode) error {
	if !nmgr.enforced() || streams.ReadsAs(suffix, nmgr.area(), mode) {
		return nil
	}
	var have []string
	for _, r := range streams.ReadersOf(suffix) {
		have = append(have, r.Area+" ("+r.Mode.String()+")")
	}
	return nmgr.undeclared("read ("+mode.String()+")", suffix, have)
}

// requireConsumer refuses a durable consumer the area does not declare: the name it is
// about to create and consume under must be one DeclaredDurables derives for this area.
func (nmgr *NatsManager) requireConsumer(suffix, durable string) error {
	if !nmgr.enforced() {
		return nil
	}
	for _, r := range streams.ReadersOf(suffix) {
		if r.Area != nmgr.area() {
			continue
		}
		if slices.Contains(DeclaredDurables(nmgr.Microservice.InstanceId, suffix, r), durable) {
			return nil
		}
	}
	return fmt.Errorf("messaging: area %q does not declare the consumer %q on stream %q: "+
		"declare the reader (and its mode) in core/streams", nmgr.area(), durable, suffix)
}

// requireBucket refuses a bucket the area is not declared to open (read or write).
func (nmgr *NatsManager) requireBucket(logical string) error {
	if !nmgr.enforced() {
		return nil
	}
	b, ok := kv.Lookup(logical)
	if !ok {
		return fmt.Errorf("messaging: KV bucket %q is not declared in core/kv: add it to kv.All "+
			"(with its disk tier) so the reservation budget accounts for it", logical)
	}
	if kv.Administers(logical, nmgr.area()) {
		return nil
	}
	return fmt.Errorf("messaging: area %q is not declared to open KV bucket %q (writers %v, readers %v): "+
		"declare it in core/kv", nmgr.area(), logical, b.Writers, b.Readers)
}

func (nmgr *NatsManager) undeclared(role, suffix string, declared []string) error {
	return fmt.Errorf("messaging: area %q is not declared to %s stream %q (declared: %v): "+
		"declare the role in core/streams, or the service is reaching into a stream that is not its own",
		nmgr.area(), role, suffix, declared)
}

// DeclaredDurables is every consumer name a declared reader may create, update, fetch from,
// acknowledge and delete on suffix, derived from the declaration so a grant can be generated
// from it and a refusal checked against it by the same function.
//
// A durable reader has one name. An ack-floor reader has two: its own, and the ordinary name
// of the durable it replaced, which it deletes at the start of a term (retireLegacyDurable).
// The device-events capture reader has two as well: its own (CaptureDurableName) and the
// ordinary name an earlier build read the stream with, which it deletes whenever it binds
// (retireLegacyCaptureDurable).
//
// An ephemeral or live reader has none: the first is named by the client library and the
// second is not a consumer.
func DeclaredDurables(instanceId, suffix string, r streams.Reader) []string {
	switch r.Mode {
	case streams.ReadDurable:
		if streams.ShapeOf(suffix) == streams.ShapeDeviceEvents {
			return []string{CaptureDurableName(instanceId, r.Area, suffix), DurableName(instanceId, r.Area, suffix)}
		}
		return []string{DurableName(instanceId, r.Area, suffix)}
	case streams.ReadAckFloor:
		return []string{AckFloorDurableName(instanceId, r.Area, suffix), DurableName(instanceId, r.Area, suffix)}
	default:
		return nil
	}
}
