// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
)

// DashboardShare names one anchor a dashboard is shared with (ADR-081): a customer or an
// access-scope group whose scoped members may list the dashboard and open its published
// version. A dashboard is shared with nobody until an author shares it; sharing is replaced
// whole by shareDashboard.
//
// The row is keyed by everything it says — tenant, dashboard, anchor — so a share is a fact
// that either exists or does not, with nothing to update. It leaves with its dashboard (a
// foreign key ON DELETE CASCADE, and DeleteDashboard's own delete) and with its customer or
// group (the entity-deleted consumer). It is tenant-scoped like every row here, and it
// declares no scope rule of its own: a scoped member never reads a share, only dashboards
// through one.
type DashboardShare struct {
	TenantId    string `gorm:"primaryKey;not null;size:128"`
	DashboardID uint   `gorm:"primaryKey;not null"`
	// AnchorKind is "customer" or "group" (auth.AnchorKindCustomer, auth.AnchorKindGroup).
	AnchorKind  string `gorm:"primaryKey;not null;size:16"`
	AnchorToken string `gorm:"primaryKey;not null;size:128"`
	CreatedAt   time.Time
}

// Anchor renders the share as a scope anchor ("customer:acme").
func (s DashboardShare) Anchor() string { return s.AnchorKind + ":" + s.AnchorToken }

// shareSet is the anchor set a dashboard's visibility to scoped members is decided by.
func shareSet(row string) rdb.AnchorSet {
	return rdb.AnchorSet{
		Model:       &DashboardShare{},
		Keys:        []rdb.KeyPair{{Set: "DashboardID", Row: row}},
		KindColumn:  "AnchorKind",
		TokenColumn: "AnchorToken",
	}
}

// ScopedBy admits a member limited to part of the tenant (ADR-081) to the dashboards shared
// with an anchor in its scope, read-only. The share rows are matched in the same tenant, by
// core/rdb, so another tenant's share can never make a dashboard visible.
func (Dashboard) ScopedBy() rdb.ScopeRule { return rdb.AnchorTable(shareSet("ID")) }

// ScopedBy admits a scoped member to the published versions of a dashboard shared with it —
// the snapshot publishedDashboard serves — and to nothing else; reading drafts and history
// stays behind dashboard:write at the resolver.
func (DashboardVersion) ScopedBy() rdb.ScopeRule { return rdb.AnchorTable(shareSet("DashboardID")) }
