// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// CustomerChecker confirms customer tokens against device-management, which owns customers.
// Missing returns the requested tokens that name no customer in the tenant; an error means the
// answer could not be obtained, and is never read as "none missing".
type CustomerChecker interface {
	Missing(ctx context.Context, tenant string, tokens []string) ([]string, error)
}

// MaxDashboardShares bounds how many anchors one dashboard is shared with: the most customer
// tokens one lookup may confirm. It is not the scope cap — a dashboard an OEM builds once is
// shared with many customers, while a member's scope names a few — and a longer list is
// refused, never truncated.
const MaxDashboardShares = rdb.MaxLookupKeys

// ErrGroupShareUnsupported refuses a share with a group: group scopes are not available yet,
// so a group anchor could not be matched by any member and would sit as an inert promise.
var ErrGroupShareUnsupported = integrity.NewRefusal(integrity.ClassInvalid,
	"a dashboard can be shared only with customers")

// ErrCustomersUnverifiable refuses a share when device-management cannot be asked whether the
// customers exist — the endpoint or the service secret is not configured. A share naming a
// customer nobody checked could grant a customer created later under that token.
var ErrCustomersUnverifiable = errors.New("dashboards cannot be shared: customers cannot be looked up, " +
	"because the device-management endpoint or service secret is not configured")

// ShareDashboard replaces the anchors the dashboard with the given token is shared with
// (ADR-081): afterwards, a member limited to part of the tenant lists the dashboard and opens
// its published version exactly when one of anchors is in its scope. An empty list unshares
// it. anchors are "customer:<token>"; every customer must exist (NOT_FOUND names the first
// that does not), a group is refused, duplicates collapse, and more than MaxDashboardShares
// is refused.
//
// The dashboard row is locked for the replacement, so two concurrent shares serialize and the
// result is one caller's list, never a mixture.
func (api *Api) ShareDashboard(ctx context.Context, token string, anchors []string) (*Dashboard, error) {
	if err := api.refuseDeletedTenant(ctx); err != nil {
		return nil, err
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return nil, core.ErrNoTenant
	}
	customers, err := shareTokens(anchors)
	if err != nil {
		return nil, err
	}
	// The dashboard first: a share of a dashboard that does not exist is NOT_FOUND for the
	// dashboard, whatever the customers are.
	if found, err := api.DashboardsByToken(ctx, []string{token}); err != nil {
		return nil, err
	} else if len(found) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	if len(customers) > 0 {
		if api.Customers == nil {
			return nil, ErrCustomersUnverifiable
		}
		missing, err := api.Customers.Missing(ctx, tenant, customers)
		if err != nil {
			return nil, fmt.Errorf("customers could not be checked: %w", err)
		}
		if len(missing) > 0 {
			return nil, fmt.Errorf("%w: customer %q does not exist", gorm.ErrRecordNotFound, missing[0])
		}
	}

	var dash *Dashboard
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		locked, err := lockDashboard(tx, token)
		if err != nil {
			return err
		}
		if err := tx.Where("dashboard_id = ?", locked.ID).Delete(&DashboardShare{}).Error; err != nil {
			return err
		}
		if len(customers) > 0 {
			now := time.Now()
			rows := make([]DashboardShare, 0, len(customers))
			for _, c := range customers {
				rows = append(rows, DashboardShare{DashboardID: locked.ID, AnchorKind: auth.AnchorKindCustomer,
					AnchorToken: c, CreatedAt: now})
			}
			if err := tx.Create(&rows).Error; err != nil {
				return err
			}
		}
		dash = locked
		return nil
	})
	if err != nil {
		return nil, err
	}
	return dash, nil
}

// shareTokens validates a share list and returns its customer tokens, deduplicated, in order.
func shareTokens(anchors []string) ([]string, error) {
	out := make([]string, 0, len(anchors))
	seen := make(map[string]struct{}, len(anchors))
	for _, a := range anchors {
		kind, token, err := auth.ParseAnchor(a)
		if err != nil {
			return nil, integrity.NewRefusal(integrity.ClassInvalid, err.Error())
		}
		if kind != auth.AnchorKindCustomer {
			return nil, ErrGroupShareUnsupported
		}
		if err := core.ValidateToken(token); err != nil {
			return nil, integrity.NewRefusal(integrity.ClassInvalid, fmt.Sprintf("customer token %q: %v", token, err))
		}
		if _, dup := seen[token]; dup {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	if len(out) > MaxDashboardShares {
		return nil, limit.Exceeded("dashboard share anchors", len(out), MaxDashboardShares)
	}
	return out, nil
}

// SharesOf returns the anchors each of the given dashboards is shared with, sorted, keyed by
// dashboard id. A dashboard shared with nobody is absent.
func (api *Api) SharesOf(ctx context.Context, dashboardIDs ...uint) (map[uint][]string, error) {
	rows := make([]DashboardShare, 0)
	if err := rdb.FindByKeys(api.RDB.DB(ctx), &rows, "dashboard_id", dashboardIDs); err != nil {
		return nil, err
	}
	out := make(map[uint][]string)
	for _, r := range rows {
		out[r.DashboardID] = append(out[r.DashboardID], r.Anchor())
	}
	for id := range out {
		sort.Strings(out[id])
	}
	return out, nil
}

// DeleteSharesOfEntity removes every share naming a deleted entity (a customer, or a group
// once group scopes exist) and reports how many it removed. Only shares made BEFORE the
// deletion are removed: a redelivered or replayed deletion must not take away a share made
// afterwards with a reused token, for a customer that is new.
func (api *Api) DeleteSharesOfEntity(ctx context.Context, entityType, token string, deletedAt time.Time) (int64, error) {
	kind := strings.ToLower(entityType)
	if kind != auth.AnchorKindCustomer && kind != auth.AnchorKindGroup {
		return 0, nil
	}
	res := api.RDB.DB(ctx).Where("anchor_kind = ? AND anchor_token = ? AND created_at <= ?", kind, token, deletedAt).
		Delete(&DashboardShare{})
	return res.RowsAffected, res.Error
}
