// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package partition is the one function from a tenant to a lane: tenant -> virtual partition
// (vpartition) -> shard group.
//
// Every tier that will one day split a single instance's work by tenant (the broker's subject
// mapping, a consumer group, an event-store cluster, a DETECT lease) has to agree on WHICH lane
// a tenant is in, or the cross-wiring costs a hop per mismatch. This package is that agreement,
// declared once, in the same spirit as core/streams: callers derive from it, they never restate
// it.
//
//	vpartition(tenant) = override[tenant] ?? (fnv1a64(instanceId + "/" + tenant) mod P)
//	shardGroup(vp)     = groups[vp]
//
// 🔴 NOTHING CONSUMES THIS YET. Today the instance is the only partition, which is the
// degenerate case of this contract: P = 64 vpartitions that all sit on shard group 0. Adding the
// package changes no routing, no subject, no durable and no query. It exists so the slices that
// do route by lane have a single, already-agreed answer to call.
//
// 🔴 P IS FIXED FOR THE INSTANCE'S LIFE. Changing P rehashes every tenant, and a tenant's lane is
// where its streams, rows and leases live; the design over-provisions P (64) precisely so that
// the number of shard groups can grow by MOVING vpartitions, never by rehashing. So P is
// recorded once (user-management's placement table) and everything that learns it afterwards
// compares and REFUSES a different value (ErrPChanged) rather than adopting it.
//
// 🔴 THE HASH IS A WIRE CONTRACT. It must give the same answer on every architecture, every Go
// version and every build, forever: a tenant whose lane moved because a toolchain changed would
// silently orphan its own data. So it is a hand-written FNV-1a over bytes with uint64
// arithmetic only (no int, no hash/maphash, no map iteration, no runtime seed), and its outputs
// are pinned in golden vectors (TestHashGoldenVectors).
package partition

import (
	"errors"
	"fmt"
	"strconv"
)

// DefaultP is the number of vpartitions an instance is created with. It is deliberately more
// than any near-term number of shard groups.
const DefaultP = 64

// MaxP bounds P. The vpartition label on per-tenant metrics has exactly P values, so P is a
// metric-cardinality ceiling as much as a placement one; and the mirror carries one entry per
// vpartition.
const MaxP = 1024

// MaxOverrides bounds how many tenants may be pinned to a vpartition by hand. Overrides are for
// the few tenants who matter, hashed placement is for the long tail, and the whole table is
// mirrored as one document that has to stay far below the broker's message limit.
const MaxOverrides = 4096

// ErrPChanged is returned wherever a P that differs from the one already recorded is presented.
// It is an error value, not a log line: a service that sees it must refuse to start.
var ErrPChanged = errors.New("partition: the vpartition count P is fixed for the life of the instance " +
	"and cannot be changed; changing it would rehash every tenant onto a different lane and orphan the " +
	"data each one already has. Restore the recorded value, or create a new instance")

const (
	fnvOffset64 uint64 = 14695981039346656037
	fnvPrime64  uint64 = 1099511628211
)

// Hash is FNV-1a (64-bit) over instanceID + "/" + tenant.
//
// The separator cannot occur in either part: both are tokens (core.ValidateToken admits only
// letters, digits, hyphen and underscore), so the concatenation is unambiguous.
func Hash(instanceID, tenant string) uint64 {
	h := fnvOffset64
	for i := 0; i < len(instanceID); i++ {
		h ^= uint64(instanceID[i])
		h *= fnvPrime64
	}
	h ^= uint64('/')
	h *= fnvPrime64
	for i := 0; i < len(tenant); i++ {
		h ^= uint64(tenant[i])
		h *= fnvPrime64
	}
	return h
}

// HashedVPartition is the vpartition a tenant HASHES to, ignoring any override. The modulo is
// taken in uint64, so the result does not depend on the width of int.
func HashedVPartition(instanceID, tenant string, p int) int {
	if p < 1 {
		panic(fmt.Sprintf("partition: P must be at least 1, got %d", p))
	}
	return int(Hash(instanceID, tenant) % uint64(p))
}

// Lane is where a tenant's work goes.
type Lane struct {
	VPartition int
	ShardGroup int
}

// Table is the placement of an instance: the vpartition count, which shard group owns each
// vpartition, and the tenants pinned to a vpartition of their own choosing.
//
// It is a plain value. user-management holds the authoritative copy in its database and mirrors
// it into a KV bucket; everything else reads the mirror.
type Table struct {
	// Format is the wire format of the table. 0 means the first format (TableFormat), so a table
	// written before this field existed, and one written today, are byte-identical: an old
	// reader that rejects unknown fields is not broken by a rolling upgrade. A reader REFUSES a
	// Format above the one it knows (a table it would half-understand) and TOLERATES additive
	// fields under a known one.
	Format int `json:"format,omitempty"`
	// P is the vpartition count. Fixed for the instance's life.
	P int `json:"p"`
	// Groups maps vpartition -> shard group; its length is P.
	Groups []int `json:"groups"`
	// Overrides maps a tenant token to the vpartition it is pinned to.
	Overrides map[string]int `json:"overrides,omitempty"`
	// Generation counts the changes made to the assignment or the overrides, so a reader can
	// tell a newer table from an older one without comparing them. A change of assignment is a
	// cutover event; this is its ordinal, not a lock.
	Generation uint64 `json:"generation"`
}

// TableFormat is the table wire format this build understands.
const TableFormat = 1

// NewTable is the degenerate placement every instance starts with: P vpartitions, all on shard
// group 0, no overrides. It is exactly today's behaviour: one lane.
func NewTable(p int) Table {
	return Table{P: p, Groups: make([]int, p)}
}

// Validate reports whether the table is internally consistent.
func (t Table) Validate() error {
	if t.Format < 0 || t.Format > TableFormat {
		return fmt.Errorf("partition: the table has format %d, this build understands up to %d", t.Format, TableFormat)
	}
	if t.P < 1 || t.P > MaxP {
		return fmt.Errorf("partition: P must be between 1 and %d, got %d", MaxP, t.P)
	}
	if len(t.Groups) != t.P {
		return fmt.Errorf("partition: the table has %d group assignments for P = %d", len(t.Groups), t.P)
	}
	for vp, g := range t.Groups {
		if g < 0 {
			return fmt.Errorf("partition: vpartition %d is assigned the negative shard group %d", vp, g)
		}
	}
	if len(t.Overrides) > MaxOverrides {
		return fmt.Errorf("partition: %d overrides exceed the limit of %d", len(t.Overrides), MaxOverrides)
	}
	for tenant, vp := range t.Overrides {
		if tenant == "" {
			return errors.New("partition: an override names no tenant")
		}
		if vp < 0 || vp >= t.P {
			return fmt.Errorf("partition: tenant %q is pinned to vpartition %d, outside [0, %d)", tenant, vp, t.P)
		}
	}
	return nil
}

// VPartition is the vpartition of a tenant: its override if it has one, else the hash.
func (t Table) VPartition(instanceID, tenant string) int {
	if vp, ok := t.Overrides[tenant]; ok {
		return vp
	}
	return HashedVPartition(instanceID, tenant, t.P)
}

// ShardGroup is the shard group that owns a vpartition. A vpartition outside the table is a
// caller's defect and panics rather than answering group 0: group 0 is a real group, so a
// plausible default would route a stray vpartition into someone's data.
func (t Table) ShardGroup(vp int) int {
	if vp < 0 || vp >= len(t.Groups) {
		panic(fmt.Sprintf("partition: vpartition %d is outside the table's %d vpartitions", vp, len(t.Groups)))
	}
	return t.Groups[vp]
}

// Lane is the vpartition and shard group of a tenant.
func (t Table) Lane(instanceID, tenant string) Lane {
	vp := t.VPartition(instanceID, tenant)
	return Lane{VPartition: vp, ShardGroup: t.ShardGroup(vp)}
}

// S is the number of shard groups the table spans: the highest assigned group plus one.
func (t Table) S() int {
	s := 0
	for _, g := range t.Groups {
		if g+1 > s {
			s = g + 1
		}
	}
	return s
}

// Equal reports whether two tables describe the same placement, generation included.
func (t Table) Equal(o Table) bool {
	if t.P != o.P || t.Generation != o.Generation || max(t.Format, 1) != max(o.Format, 1) || len(t.Groups) != len(o.Groups) || len(t.Overrides) != len(o.Overrides) {
		return false
	}
	for i := range t.Groups {
		if t.Groups[i] != o.Groups[i] {
			return false
		}
	}
	for k, v := range t.Overrides {
		if ov, ok := o.Overrides[k]; !ok || ov != v {
			return false
		}
	}
	return true
}

// CheckP refuses a P that differs from the recorded one.
func CheckP(recorded, presented int) error {
	if recorded != presented {
		return fmt.Errorf("%w (recorded %d, presented %d)", ErrPChanged, recorded, presented)
	}
	return nil
}

// Label renders a vpartition as a metric label value. There are at most P of them, a bounded
// set; and because a tenant's vpartition is a function of the tenant, adding the label to a
// series already keyed by tenant adds no series.
func Label(vp int) string { return strconv.Itoa(vp) }
