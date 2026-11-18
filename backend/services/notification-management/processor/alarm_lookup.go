// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/rs/zerolog/log"
)

// AlarmLookup returns the states of existing alarms, omitting tokens definitely absent.
// An error must never be represented as an empty successful result.
type AlarmLookup interface {
	LookupAlarms(context.Context, []string) (map[string]string, error)
}

const alarmLifecyclesDocument = `query($tokens: [String!]!) { alarmLifecycles(tokens: $tokens) { token state } }`

type alarmLookup struct {
	client *svcclient.Client
	url    string
}

func NewAlarmLookup(client *svcclient.Client, url string) AlarmLookup {
	return &alarmLookup{client: client, url: url}
}

func (s *alarmLookup) LookupAlarms(ctx context.Context, tokens []string) (map[string]string, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return nil, core.ErrNoTenant
	}
	var out struct {
		AlarmLifecycles *[]struct {
			Token string
			State string
		} `json:"alarmLifecycles"`
	}
	err := s.client.Query(ctx, s.url, tenant,
		alarmLifecyclesDocument,
		map[string]any{"tokens": tokens}, &out)
	if err != nil {
		return nil, err
	}
	if out.AlarmLifecycles == nil {
		return nil, fmt.Errorf("device-management returned no alarm lookup result")
	}
	found := make(map[string]string, len(*out.AlarmLifecycles))
	for _, a := range *out.AlarmLifecycles {
		if a.Token == "" || (a.State != "ACTIVE" && a.State != "CLEARED") {
			return nil, fmt.Errorf("invalid alarm lookup result")
		}
		found[a.Token] = a.State
	}
	return found, nil
}

func (n *PolicyNotifier) lookupAlarms(ctx context.Context, tokens []string) (map[string]string, error) {
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	found, err := n.AlarmLookup.LookupAlarms(lctx, tokens)
	if err != nil {
		if n.lookupErrors != nil {
			n.lookupErrors.Inc()
		}
		log.Warn().Err(err).Msg("Alarm backstop lookup failed; leaving notification lifecycles open")
	}
	return found, err
}

func (n *PolicyNotifier) endOrphan(ctx context.Context, state *model.NotificationState, now time.Time) error {
	return n.api.MarkCleared(ctx, state.AlarmToken, state.AlarmKey, state.Severity, state.CycleRaisedAt.Time, now)
}

// OrphanAlarmHorizon avoids racing a recent alarm's creation and notification delivery.
const OrphanAlarmHorizon = time.Hour

// SweepOrphans reconciles old OPEN states in tenant-local keyset pages, including states
// that never paged. Recent re-raises are excluded even when the row itself is old.
func (n *PolicyNotifier) SweepOrphans(ctx context.Context, now time.Time) error {
	if n.AlarmLookup == nil || n.tenantDeleted(ctx) {
		return nil
	}
	var after uint
	for {
		var states []*model.NotificationState
		before := now.Add(-OrphanAlarmHorizon)
		err := n.api.RDB.DB(ctx).Where("id > ? AND cleared_at IS NULL AND acknowledged_at IS NULL AND created_at < ? AND (cycle_raised_at IS NULL OR cycle_raised_at < ?)", after, before, before).
			Order("id").Limit(rdb.MaxLookupKeys).Find(&states).Error
		if err != nil {
			return err
		}
		if len(states) == 0 {
			return nil
		}
		tokens := make([]string, 0, len(states))
		for _, state := range states {
			tokens = append(tokens, state.AlarmToken)
		}
		found, err := n.lookupAlarms(ctx, tokens)
		if err != nil {
			return err
		} // An error ends nothing in this chunk.
		for _, state := range states {
			if found[state.AlarmToken] == "" || found[state.AlarmToken] == "CLEARED" {
				if err := n.endOrphan(ctx, state, now); err != nil {
					return err
				}
			}
		}
		after = states[len(states)-1].ID
	}
}
