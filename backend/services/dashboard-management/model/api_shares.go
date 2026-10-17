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

// ShareChecker confirms the anchors a share names against device-management, which owns
// customers and groups. Missing returns the requested tokens of the anchor kind
// (auth.AnchorKindCustomer or auth.AnchorKindGroup) that name nothing a dashboard can be shared
// with in the tenant: a customer that does not exist, or a group that does not exist or is not
// an access scope. An error means the answer could not be obtained, and is never read as "none
// missing".
type ShareChecker interface {
	Missing(ctx context.Context, tenant, kind string, tokens []string) ([]string, error)
}

// MaxDashboardShares bounds how many anchors one dashboard is shared with: the most tokens one
// lookup may confirm. It is not the scope cap — a dashboard an OEM builds once is shared with
// many customers, while a member's scope names a few — and a longer list is refused, never
// truncated.
const MaxDashboardShares = rdb.MaxLookupKeys

// ErrSharesUnverifiable refuses a share when device-management cannot be asked whether the
// customers and groups it names exist — the endpoint or the service secret is not configured.
// A share naming a customer nobody checked could grant a customer created later under that
// token, and one naming a group nobody checked, a group that is not an access scope.
var ErrSharesUnverifiable = errors.New("dashboards cannot be shared: customers and groups cannot be looked up, " +
	"because the device-management endpoint or service secret is not configured")

// shareAnchor is one validated anchor of a share list.
type shareAnchor struct{ kind, token string }

// ShareDashboard replaces the anchors the dashboard with the given token is shared with
// (ADR-081): afterwards, a member limited to part of the tenant lists the dashboard and opens
// its published version exactly when one of anchors is in its scope. An empty list unshares
// it. anchors are "customer:<token>" or "group:<token>"; every customer must exist, and every
// group must exist in the tenant and be an access scope (NOT_FOUND names the first that is
// not, and a group that is not an access scope answers exactly as one that does not exist).
// Duplicates collapse, and more than MaxDashboardShares is refused.
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
	shares, err := shareAnchors(anchors)
	if err != nil {
		return nil, err
	}
	// The dashboard first: a share of a dashboard that does not exist is NOT_FOUND for the
	// dashboard, whatever the anchors are.
	if found, err := api.DashboardsByToken(ctx, []string{token}); err != nil {
		return nil, err
	} else if len(found) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	if err := api.confirmShareTargets(ctx, tenant, shares); err != nil {
		return nil, err
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
		if len(shares) > 0 {
			now := time.Now()
			rows := make([]DashboardShare, 0, len(shares))
			for _, s := range shares {
				rows = append(rows, DashboardShare{DashboardID: locked.ID, AnchorKind: s.kind,
					AnchorToken: s.token, CreatedAt: now})
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

// confirmShareTargets asks device-management about every anchor of the list, one lookup per
// kind, customers first. A list that names nobody needs nobody's answer.
func (api *Api) confirmShareTargets(ctx context.Context, tenant string, shares []shareAnchor) error {
	if len(shares) == 0 {
		return nil
	}
	if api.ShareTargets == nil {
		return ErrSharesUnverifiable
	}
	for _, kind := range []string{auth.AnchorKindCustomer, auth.AnchorKindGroup} {
		var tokens []string
		for _, s := range shares {
			if s.kind == kind {
				tokens = append(tokens, s.token)
			}
		}
		if len(tokens) == 0 {
			continue
		}
		missing, err := api.ShareTargets.Missing(ctx, tenant, kind, tokens)
		if err != nil {
			return fmt.Errorf("%ss could not be checked: %w", kind, err)
		}
		if len(missing) == 0 {
			continue
		}
		if kind == auth.AnchorKindGroup {
			return fmt.Errorf("%w: group %q does not exist or is not an access scope", gorm.ErrRecordNotFound, missing[0])
		}
		return fmt.Errorf("%w: customer %q does not exist", gorm.ErrRecordNotFound, missing[0])
	}
	return nil
}

// shareAnchors validates a share list and returns its anchors, deduplicated, in order.
func shareAnchors(anchors []string) ([]shareAnchor, error) {
	out := make([]shareAnchor, 0, len(anchors))
	seen := make(map[shareAnchor]struct{}, len(anchors))
	for _, a := range anchors {
		kind, token, err := auth.ParseAnchor(a)
		if err != nil {
			return nil, integrity.NewRefusal(integrity.ClassInvalid, err.Error())
		}
		if err := core.ValidateToken(token); err != nil {
			return nil, integrity.NewRefusal(integrity.ClassInvalid, fmt.Sprintf("%s token %q: %v", kind, token, err))
		}
		s := shareAnchor{kind: kind, token: token}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
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

// DeleteSharesOfEntity removes every share naming a deleted entity (a customer or a group)
// and reports how many it removed. Only shares made BEFORE the
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
