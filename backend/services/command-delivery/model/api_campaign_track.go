// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// CampaignCycleCounts is the tracking summary of one cycle: where each of its elements
// stands, and what the commands of the dispatched ones did.
type CampaignCycleCounts struct {
	Pending    int
	Claimed    int
	Dispatched int
	Refused    int
	Skipped    int

	// Command counts over the DISPATCHED elements, by what their latest command did. A
	// retried element is counted by its retry's command, not the original's.
	Succeeded int
	// InFlight: the command exists and has not finished (queued, held, parked, sent).
	InFlight int
	// Failed: finished without success (NOT_DELIVERED, REJECTED).
	Failed int
	// Unknown: finished with an outcome the platform cannot vouch for (UNKNOWN).
	Unknown int
	// Waiting is how many in-flight commands are held for a maintenance window.
	Waiting int
}

// CampaignCycles lists a campaign's cycles, newest first.
func (api *Api) CampaignCycles(ctx context.Context, campaignToken string, limit int) ([]*CampaignCycle, error) {
	c, err := api.campaignByToken(ctx, campaignToken)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	var out []*CampaignCycle
	err = api.RDB.DB(ctx).Where("campaign_id = ?", c.ID).Order("id DESC").Limit(limit).Find(&out).Error
	return out, err
}

func (api *Api) cycleOf(ctx context.Context, c *Campaign, cycleKey string) (*CampaignCycle, error) {
	var cy CampaignCycle
	if err := api.RDB.DB(ctx).Where("campaign_id = ? AND cycle_key = ?", c.ID, cycleKey).First(&cy).Error; err != nil {
		return nil, err
	}
	return &cy, nil
}

// CampaignCycleSummary counts a cycle's elements and the commands behind them.
func (api *Api) CampaignCycleSummary(ctx context.Context, campaignToken, cycleKey string) (*CampaignCycleCounts, error) {
	c, err := api.campaignByToken(ctx, campaignToken)
	if err != nil {
		return nil, err
	}
	cy, err := api.cycleOf(ctx, c, cycleKey)
	if err != nil {
		return nil, err
	}
	db := api.RDB.DB(ctx)
	counts := &CampaignCycleCounts{}

	var byStatus []struct {
		Status string
		N      int
	}
	if err := db.Model(&CampaignElement{}).Select("status, count(*) AS n").
		Where("cycle_id = ?", cy.ID).Group("status").Scan(&byStatus).Error; err != nil {
		return nil, err
	}
	for _, r := range byStatus {
		switch CampaignElementStatus(r.Status) {
		case ElementPending:
			counts.Pending = r.N
		case ElementClaimed:
			counts.Claimed = r.N
		case ElementDispatched:
			counts.Dispatched = r.N
		case ElementRefused:
			counts.Refused = r.N
		case ElementSkipped:
			counts.Skipped = r.N
		}
	}

	// The commands of this cycle's batches, counted once each by device. Walked per batch
	// token so one query is bounded by one batch, never by the fleet.
	var batches []string
	if err := db.Model(&CampaignElement{}).Where("cycle_id = ? AND batch_token IS NOT NULL", cy.ID).
		Distinct().Pluck("batch_token", &batches).Error; err != nil {
		return nil, err
	}
	var retried []CampaignElement
	if err := db.Where("cycle_id = ? AND retry_batch_token IS NOT NULL", cy.ID).Find(&retried).Error; err != nil {
		return nil, err
	}
	supersededBy := map[string]string{} // "batch|device" -> retry batch
	retryBatches := map[string]struct{}{}
	for _, e := range retried {
		supersededBy[e.BatchToken.String+"|"+e.DeviceToken] = e.RetryBatchToken.String
		retryBatches[e.RetryBatchToken.String] = struct{}{}
	}
	sort.Strings(batches)
	for _, b := range batches {
		if err := api.countBatchCommands(ctx, b, counts, func(device string) bool {
			_, gone := supersededBy[b+"|"+device]
			return gone
		}); err != nil {
			return nil, err
		}
	}
	for rb := range retryBatches {
		if err := api.countBatchCommands(ctx, rb, counts, nil); err != nil {
			return nil, err
		}
	}
	return counts, nil
}

func (api *Api) countBatchCommands(ctx context.Context, batchToken string, counts *CampaignCycleCounts,
	skip func(device string) bool) error {
	var rows []Command
	if err := api.RDB.DB(ctx).Select("device_token", "status", "outcome", "dispatch_after").
		Where("batch_token = ?", batchToken).Find(&rows).Error; err != nil {
		return err
	}
	now := time.Now()
	for _, r := range rows {
		if skip != nil && skip(r.DeviceToken) {
			continue
		}
		switch {
		case !CommandStatus(r.Status).Terminal():
			counts.InFlight++
			if r.DispatchAfter.Valid && r.DispatchAfter.Time.After(now) {
				counts.Waiting++
			}
		case CommandOutcome(r.Outcome.String) == OutcomeSucceeded:
			counts.Succeeded++
		case CommandOutcome(r.Outcome.String) == OutcomeUnknown:
			counts.Unknown++
		default:
			counts.Failed++
		}
	}
	return nil
}

// CampaignElementView is an element with the state of the command behind it.
type CampaignElementView struct {
	Element CampaignElement
	// CommandStatus and CommandOutcome are those of the latest command for the element, empty
	// when it has none (not yet dispatched, refused, skipped).
	CommandStatus  string
	CommandOutcome string
}

// CampaignElements pages through a cycle's elements in id order. status filters when set.
func (api *Api) CampaignElements(ctx context.Context, campaignToken, cycleKey, status string,
	afterId uint, limit int) ([]*CampaignElementView, error) {
	c, err := api.campaignByToken(ctx, campaignToken)
	if err != nil {
		return nil, err
	}
	cy, err := api.cycleOf(ctx, c, cycleKey)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 1000 {
		limit = 200
	}
	q := api.RDB.DB(ctx).Where("cycle_id = ? AND id > ?", cy.ID, afterId)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var rows []CampaignElement
	if err := q.Order("id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	// One command lookup per distinct effective batch on the page.
	devicesByBatch := map[string][]string{}
	for _, e := range rows {
		if b := effectiveBatch(e); b != "" {
			devicesByBatch[b] = append(devicesByBatch[b], e.DeviceToken)
		}
	}
	type state struct{ status, outcome string }
	states := map[string]state{}
	for b, devices := range devicesByBatch {
		for start := 0; start < len(devices); start += campaignRowChunk {
			end := min(start+campaignRowChunk, len(devices))
			var cmds []Command
			if err := api.RDB.DB(ctx).Select("device_token", "status", "outcome").
				Where("batch_token = ? AND device_token IN ?", b, devices[start:end]).Find(&cmds).Error; err != nil {
				return nil, err
			}
			for _, cmd := range cmds {
				states[b+"|"+cmd.DeviceToken] = state{cmd.Status, cmd.Outcome.String}
			}
		}
	}
	out := make([]*CampaignElementView, 0, len(rows))
	for _, e := range rows {
		v := &CampaignElementView{Element: e}
		if st, ok := states[effectiveBatch(e)+"|"+e.DeviceToken]; ok {
			v.CommandStatus, v.CommandOutcome = st.status, st.outcome
		}
		out = append(out, v)
	}
	return out, nil
}

func effectiveBatch(e CampaignElement) string {
	if e.RetryBatchToken.Valid {
		return e.RetryBatchToken.String
	}
	if e.Status == string(ElementDispatched) && e.BatchToken.Valid {
		return e.BatchToken.String
	}
	return ""
}

// CampaignRetryResult reports a retry-from-failed.
type CampaignRetryResult struct {
	// Batches are the retry batches created (or replayed), one per original batch.
	Batches []string
	// Retried is how many elements were handed to a retry.
	Retried int
	// Held counts elements left alone because their command is not safe to send again
	// (UNKNOWN without includeUnknown, or still in flight).
	Held int
}

// RetryCampaignFailed re-sends a cycle's command to the elements that did not succeed,
// through the platform's batch retry, so every safeguard of a retry applies: a device whose
// command is still in flight, or already succeeded, or whose outcome is unknown (unless the
// caller acknowledges it), is never sent a second copy.
//
// Re-running it is safe: a retry token is derived from the original batch and the number of
// retries it already has, and an element already handed to a retry is not offered again.
func (api *Api) RetryCampaignFailed(ctx context.Context, campaignToken, cycleKey string, includeUnknown bool) (*CampaignRetryResult, error) {
	c, err := api.campaignByToken(ctx, campaignToken)
	if err != nil {
		return nil, err
	}
	cy, err := api.cycleOf(ctx, c, cycleKey)
	if err != nil {
		return nil, err
	}
	var batches []string
	if err := api.RDB.DB(ctx).Model(&CampaignElement{}).
		Where("cycle_id = ? AND batch_token IS NOT NULL", cy.ID).Distinct().Pluck("batch_token", &batches).Error; err != nil {
		return nil, err
	}
	sort.Strings(batches)
	result := &CampaignRetryResult{}
	var errs []error
	for _, b := range batches {
		if err := api.retryCampaignBatch(ctx, b, cy, includeUnknown, result); err != nil {
			errs = append(errs, fmt.Errorf("batch %s: %w", b, err))
		}
	}
	return result, errors.Join(errs...)
}

func (api *Api) retryCampaignBatch(ctx context.Context, batchToken string, cy *CampaignCycle,
	includeUnknown bool, result *CampaignRetryResult) error {
	// A run that created a retry and died before recording it must not be repeated: the retry's
	// commands already exist, and a second would send them again. Adopt them first.
	if err := api.adoptExistingRetries(ctx, batchToken, cy); err != nil {
		return err
	}
	var elements []CampaignElement
	if err := api.RDB.DB(ctx).Where("cycle_id = ? AND batch_token = ? AND retry_batch_token IS NULL", cy.ID, batchToken).
		Order("id ASC").Find(&elements).Error; err != nil {
		return err
	}
	original, found, err := api.liveBatchByToken(ctx, batchToken)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	refusalsKnown, err := completeRefusals(original)
	if err != nil {
		return err
	}
	var cmds []Command
	if err := api.RDB.DB(ctx).Select("device_token", "status", "outcome").
		Where("batch_token = ?", batchToken).Find(&cmds).Error; err != nil {
		return err
	}
	byDevice := map[string]Command{}
	for _, cmd := range cmds {
		byDevice[cmd.DeviceToken] = cmd
	}

	var devices []string
	retriable := map[string]CampaignElement{}
	for _, e := range elements {
		switch CampaignElementStatus(e.Status) {
		case ElementRefused:
			// Only a refusal the batch's record holds completely: RetryCommandBatch admits a
			// refused device only on that evidence, and naming one it cannot prove fails the
			// whole retry. KNOWN LIMIT: past 100 refusals of one code the record is a sample,
			// so those elements stay REFUSED and are not offered here.
			if refusalsKnown[e.DeviceToken] {
				devices = append(devices, e.DeviceToken)
				retriable[e.DeviceToken] = e
			}
		case ElementDispatched:
			cmd, ok := byDevice[e.DeviceToken]
			if !ok {
				continue
			}
			if !CommandStatus(cmd.Status).Terminal() {
				result.Held++
				continue
			}
			switch CommandOutcome(cmd.Outcome.String) {
			case OutcomeSucceeded:
			case OutcomeNotDelivered, OutcomeRejected:
				devices = append(devices, e.DeviceToken)
				retriable[e.DeviceToken] = e
			default:
				if includeUnknown {
					devices = append(devices, e.DeviceToken)
					retriable[e.DeviceToken] = e
				} else {
					result.Held++
				}
			}
		}
	}
	if len(devices) == 0 {
		return nil
	}
	prior, err := api.RetriesOfBatch(ctx, batchToken)
	if err != nil {
		return err
	}
	retryToken := fmt.Sprintf("%s-r%d", batchToken, len(prior)+1)
	retry, err := api.RetryCommandBatch(ctx, &CommandBatchRetryRequest{
		Token: retryToken, Of: batchToken, DeviceTokens: devices,
		IncludeUnknown: includeUnknown, AllowPartial: true,
	})
	if err != nil {
		return err
	}
	var admitted []Command
	if err := api.RDB.DB(ctx).Select("device_token").Where("batch_token = ?", retry.Token).Find(&admitted).Error; err != nil {
		return err
	}
	in := map[string]struct{}{}
	for _, a := range admitted {
		in[a.DeviceToken] = struct{}{}
	}
	var handed []uint
	var revived []uint
	for _, d := range devices {
		if _, ok := in[d]; !ok {
			continue
		}
		e := retriable[d]
		handed = append(handed, e.ID)
		if CampaignElementStatus(e.Status) == ElementRefused {
			revived = append(revived, e.ID)
		}
	}
	for start := 0; start < len(handed); start += campaignRowChunk {
		end := min(start+campaignRowChunk, len(handed))
		if err := api.RDB.DB(ctx).Model(&CampaignElement{}).Where("id IN ?", handed[start:end]).
			Updates(map[string]any{"retry_batch_token": retry.Token, "updated_at": time.Now()}).Error; err != nil {
			return err
		}
	}
	for start := 0; start < len(revived); start += campaignRowChunk {
		end := min(start+campaignRowChunk, len(revived))
		if err := api.RDB.DB(ctx).Model(&CampaignElement{}).Where("id IN ?", revived[start:end]).
			Updates(map[string]any{"status": string(ElementDispatched), "reason": nil}).Error; err != nil {
			return err
		}
	}
	result.Batches = append(result.Batches, retry.Token)
	result.Retried += len(handed)
	return nil
}

// adoptExistingRetries records, on the elements of the original batch, every retry batch that
// already carries a command for them and that no element points at yet. It is what makes a
// retry that crashed between creating its batch and recording it harmless to run again.
func (api *Api) adoptExistingRetries(ctx context.Context, batchToken string, cy *CampaignCycle) error {
	retries, err := api.RetriesOfBatch(ctx, batchToken)
	if err != nil {
		return err
	}
	for _, rt := range retries {
		var cmds []Command
		if err := api.RDB.DB(ctx).Select("device_token").Where("batch_token = ?", rt).Find(&cmds).Error; err != nil {
			return err
		}
		devices := make([]string, 0, len(cmds))
		for _, c := range cmds {
			devices = append(devices, c.DeviceToken)
		}
		for start := 0; start < len(devices); start += campaignRowChunk {
			end := min(start+campaignRowChunk, len(devices))
			if err := api.RDB.DB(ctx).Model(&CampaignElement{}).
				Where("cycle_id = ? AND batch_token = ? AND retry_batch_token IS NULL AND device_token IN ?",
					cy.ID, batchToken, devices[start:end]).
				Updates(map[string]any{"retry_batch_token": rt, "status": string(ElementDispatched),
					"reason": nil, "updated_at": time.Now()}).Error; err != nil {
				return err
			}
		}
	}
	return nil
}
