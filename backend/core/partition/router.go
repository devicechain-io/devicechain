// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package partition

import (
	"errors"
	"fmt"
	"sync"
)

// ErrNoPlacement is returned by Router before any placement has been loaded. It is an error
// rather than shard group 0: group 0 is a real group, and routing an unplaced tenant to it would
// put the tenant's rows in someone else's database.
var ErrNoPlacement = errors.New("partition: no placement loaded yet; refusing to route")

// ErrNoTenant is returned by Router when asked to route an empty tenant.
var ErrNoTenant = errors.New("partition: a tenant is required to route")

// Router answers tenant -> shard group from the latest placement table. It is the reader side of
// the mirror: a service builds one, feeds it from the KV bucket, and hands it to whatever routes
// by lane. The hot path is one atomic load and a hash; it never touches a database.
type Router struct {
	instanceID string

	mu    sync.RWMutex
	table *Table
	// bad is set when the mirror holds a table this reader cannot adopt (it does not parse, or
	// disagrees with the one held). Routing then refuses rather than keep answering from a
	// placement that may be out of date: a newer release may already be routing a moved tenant
	// elsewhere. A later good Update clears it.
	bad error
}

// NewRouter returns a Router with no placement; routing refuses until Update or Refresh
// succeeds.
func NewRouter(instanceID string) *Router { return &Router{instanceID: instanceID} }

// Update adopts t if it is valid, has the same P as the one held, and is not older. The same
// generation is accepted only if it is identical, so two authorities that disagree are noticed.
func (r *Router) Update(t Table) error {
	if err := t.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur := r.table; cur != nil {
		if cur.P != t.P {
			return ErrPChanged
		}
		if t.Generation < cur.Generation {
			return ErrStale
		}
		if t.Generation == cur.Generation && !t.Equal(*cur) {
			return ErrDivergent
		}
	}
	cp := t
	cp.Groups = append([]int(nil), t.Groups...)
	if t.Overrides != nil {
		cp.Overrides = make(map[string]int, len(t.Overrides))
		for k, v := range t.Overrides {
			cp.Overrides[k] = v
		}
	}
	r.table = &cp
	r.bad = nil
	return nil
}

// Refresh loads the mirrored table and adopts it. A failure leaves the previous placement in
// force: right for a transient read error, and loud (ErrPChanged, ErrDivergent) for a bad table.
//
// A transient read failure, an empty mirror and a merely older table keep the placement in force.
// A table that does not parse, or that this reader refuses (changed P, a divergent table), makes
// routing refuse until a good table arrives.
func (r *Router) Refresh(b Bucket) error {
	raw, _, found, err := b.Get(TableKey)
	if err != nil {
		return fmt.Errorf("partition: reading the mirrored table: %w", err)
	}
	if !found {
		return ErrNoTable
	}
	t, err := Decode(raw)
	if err != nil {
		r.fail(err)
		return err
	}
	if err := r.Update(t); err != nil {
		if !errors.Is(err, ErrStale) {
			r.fail(err)
		}
		return err
	}
	return nil
}

func (r *Router) fail(err error) {
	r.mu.Lock()
	r.bad = fmt.Errorf("partition: routing refused until a compatible placement is mirrored: %w", err)
	r.mu.Unlock()
}

// Lane is the lane of tenant under the current placement.
func (r *Router) Lane(tenant string) (Lane, error) {
	r.mu.RLock()
	t, bad := r.table, r.bad
	r.mu.RUnlock()
	if bad != nil {
		return Lane{}, bad
	}
	if t == nil {
		return Lane{}, ErrNoPlacement
	}
	if tenant == "" {
		return Lane{}, ErrNoTenant
	}
	return t.Lane(r.instanceID, tenant), nil
}

// ShardGroup implements rdb.ShardResolver.
func (r *Router) ShardGroup(tenant string) (int, error) {
	l, err := r.Lane(tenant)
	if err != nil {
		return 0, err
	}
	return l.ShardGroup, nil
}
