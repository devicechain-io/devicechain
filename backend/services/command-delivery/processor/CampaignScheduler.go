// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/core"
)

// CampaignTickInterval is how often the scheduler looks for campaign work. It is the
// granularity of a schedule: a fixture whose dusk is 18:03:20 is commanded on the first
// pass after that, so at most this long late. A fixed platform value rather than a setting,
// because a campaign's own lateness bound (not this) is what an operator tunes.
const CampaignTickInterval = 15 * time.Second

// CampaignPruneInterval is how often a replica prunes campaign history. History is not urgent,
// and pruning on every tick would put a delete sweep beside every firing.
const CampaignPruneInterval = 10 * time.Minute

// CampaignScheduler runs the campaign tick in EVERY replica. There is no leader and none is
// needed: each step of a pass is a compare-and-set the database arbitrates (see
// model.Api.TickCampaigns), which is the arrangement the notification escalation scheduler
// uses and for the same reason: a lease would add a failure mode (a stuck holder, a skewed
// clock) without removing the need for the claim.
type CampaignScheduler struct {
	*core.PeriodicTask
	Api *model.Api

	// lastPrune is when this replica last pruned. Only runOnce reads or writes it, and the
	// periodic task runs one pass at a time.
	lastPrune time.Time
}

// NewCampaignScheduler builds the scheduler.
func NewCampaignScheduler(ms *core.Microservice, api *model.Api, callbacks core.LifecycleCallbacks) *CampaignScheduler {
	s := &CampaignScheduler{Api: api}
	s.PeriodicTask = core.NewPeriodicTask(ms.FunctionalArea, "campaign-scheduler", CampaignTickInterval,
		s.runOnce, callbacks, core.WithPassMetrics(ms.NewPeriodicTaskMetrics("campaign_scheduler")))
	return s
}

func (s *CampaignScheduler) runOnce(ctx context.Context) error {
	err := s.Api.TickCampaigns(ctx, time.Now(), newPassID())
	if now := time.Now(); now.Sub(s.lastPrune) >= CampaignPruneInterval {
		s.lastPrune = now
		err = errors.Join(err, s.Api.PruneCampaignHistory(ctx, now))
	}
	return err
}

// newPassID names one pass, so a claim can say who made it. It need only be unique among
// concurrent passes, not unguessable.
func newPassID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return time.Now().Format("150405.000000000")
	}
	return hex.EncodeToString(b[:])
}
