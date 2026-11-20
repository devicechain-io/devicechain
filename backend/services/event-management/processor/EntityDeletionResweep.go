// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/core"
)

const EntityDeletionResweepInterval = time.Minute

// EntityDeletionResweep closes the overlap between a persist transaction and the
// watermark commit, and recovers cleanup interrupted after that commit. Each
// replica may do the same work: cleanup is idempotent and clearing is conditional.
type EntityDeletionResweep struct {
	*core.PeriodicTask
	Api *model.Api
}

func NewEntityDeletionResweep(ms *core.Microservice, api *model.Api, callbacks core.LifecycleCallbacks) *EntityDeletionResweep {
	s := &EntityDeletionResweep{Api: api}
	s.PeriodicTask = core.NewPeriodicTask(ms.FunctionalArea, "entity-deletion-resweep", EntityDeletionResweepInterval,
		s.runOnce, callbacks, ms.NewPeriodicTaskMetrics("entity_deletion_resweep"))
	return s
}

func (s *EntityDeletionResweep) runOnce(ctx context.Context) error {
	due, err := s.Api.DueEntityDeletionWatermarks(ctx, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("listing due entity deletion watermarks: %w", err)
	}
	var failures []error
	for _, w := range due {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.Api.ResweepEntityDeletionWatermark(ctx, w); err != nil {
			failures = append(failures, fmt.Errorf("resweeping tenant %q entity %s/%s: %w", w.TenantId, w.EntityType, w.EntityToken, err))
		}
	}
	return errors.Join(failures...)
}
