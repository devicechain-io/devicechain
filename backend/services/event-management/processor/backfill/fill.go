// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package backfill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-event-management/model"
)

// rosterError marks a failure to read the roster from device-management: the job stays
// RUNNING and is retried, and fails only after dmDownLimit.
type rosterError struct{ err error }

func (e *rosterError) Error() string { return "reading the device roster: " + e.err.Error() }
func (e *rosterError) Unwrap() error { return e.err }

func logJob(ev *zerolog.Event, job *model.CalculatedBackfill) *zerolog.Event {
	return ev.Str("tenant", job.TenantId).Str("job", job.Token).Str("profile", job.ProfileToken).
		Int64("version", job.ProfileVersion).Str("phase", job.Phase).Int64("rosterAfter", job.RosterAfterId).
		Int64("samples", job.Samples).Int64("written", job.Written).Int64("alreadyPresent", job.AlreadyPresent).
		Int64("devicesDone", job.DevicesDone)
}

func decodeStrings(raw []byte) ([]string, error) {
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// acc accumulates what has been done but not yet committed. It is added to the job only when
// the transaction carrying it commits, so a rolled-back batch leaves it to be done again and
// counted once.
type acc struct {
	d     model.BackfillDelta
	skips map[string]int64 // increments by skip reason
}

func newAcc() *acc { return &acc{skips: map[string]int64{}} }

func (a *acc) add(o *acc) {
	a.d.Samples += o.d.Samples
	a.d.Computed += o.d.Computed
	a.d.Written += o.d.Written
	a.d.AlreadyPresent += o.d.AlreadyPresent
	a.d.WindowsCompressed += o.d.WindowsCompressed
	a.d.WindowsBeyondRetention += o.d.WindowsBeyondRetention
	for k, v := range o.skips {
		a.skips[k] += v
	}
}

func (a *acc) empty() bool {
	return a.d.Samples == 0 && a.d.Computed == 0 && a.d.Written == 0 && a.d.AlreadyPresent == 0 &&
		a.d.WindowsCompressed == 0 && a.d.WindowsBeyondRetention == 0 && len(a.skips) == 0
}

// delta renders the accumulation as the commit's delta, the skip map as the job's whole map
// after it.
func (a *acc) delta(base map[string]int64) model.BackfillDelta {
	d := a.d
	if len(a.skips) > 0 {
		merged := make(map[string]int64, len(base)+len(a.skips))
		for k, v := range base {
			merged[k] = v
		}
		for k, v := range a.skips {
			merged[k] += v
		}
		d.Skipped = merged
	}
	return d
}

// applied moves the job's in-memory counters to where a committed delta left them.
func (r *jobRun) applied(d model.BackfillDelta) {
	j := r.job
	j.DevicesDone += d.DevicesDone
	j.Samples += d.Samples
	j.Computed += d.Computed
	j.Written += d.Written
	j.AlreadyPresent += d.AlreadyPresent
	j.WindowsCompressed += d.WindowsCompressed
	j.WindowsBeyondRetention += d.WindowsBeyondRetention
	j.WindowsRefreshed += d.WindowsRefreshed
	if d.Skipped != nil {
		r.skipped = d.Skipped
	}
	if d.Phase != nil {
		j.Phase = *d.Phase
	}
	if d.RosterAfterId != nil {
		j.RosterAfterId = *d.RosterAfterId
	}
	if d.CursorWindow != nil {
		j.CursorWindow = *d.CursorWindow
	}
	if d.RefreshCursor != nil {
		j.RefreshCursor = *d.RefreshCursor
	}
	if d.DevicesTotal != nil {
		t := *d.DevicesTotal
		j.DevicesTotal = &t
	}
}

// commit writes a delta in its own transaction and, on success, applies it to the job.
func (r *jobRun) commit(d model.BackfillDelta) error {
	err := r.db.Transaction(func(tx *gorm.DB) error {
		return model.CommitBackfillProgress(tx, r.job.Token, r.job.ClaimEpoch, d)
	})
	if err != nil {
		return err
	}
	r.applied(d)
	return nil
}

// fillBatch processes the next device batch across its windows, newest first, and reports
// whether the FILL phase is over (it moves the job to REFRESH, so it never reports the job done).
func (r *jobRun) fillBatch(ctx context.Context) (bool, error) {
	w := r.w
	job := r.job
	started := w.now()
	page, err := w.roster.Page(ctx, job.TenantId, job.ProfileToken, r.doc.ConstantKeys, r.filter,
		job.RosterAfterId, deviceBatch)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, &rosterError{err: err}
	}
	delete(w.dmDownSince, job.Token)

	pending := newAcc()
	var evals []*model.PlanEvaluator
	defer func() {
		for _, e := range evals {
			e.Release()
		}
	}()
	if len(page.Entries) > 0 {
		windows, err := chunkWindows(r.db, job.RangeFrom, job.RangeTo)
		if err != nil {
			return false, err
		}
		for _, e := range page.Entries {
			ev, err := model.NewPlanEvaluator(r.plan, r.keys, e.Constants)
			if err != nil {
				return false, &failJob{msg: fmt.Sprintf("the frozen plan cannot run: %v", err)}
			}
			evals = append(evals, ev)
		}
		for _, win := range windows {
			// Windows come newest first; those at or after the cursor were done.
			if job.CursorWindow != nil && !win.Start.Before(*job.CursorWindow) {
				continue
			}
			if err := ctx.Err(); err != nil {
				return false, err
			}
			start := win.Start
			cursor := &start
			switch {
			case r.beyondRetention(win):
				pending.d.WindowsBeyondRetention++
				w.metrics.WindowsSkipped.WithLabelValues("retention").Inc()
				pending.d.CursorWindow = &cursor
				continue
			case win.Compressed:
				pending.d.WindowsCompressed++
				w.metrics.WindowsSkipped.WithLabelValues("compressed").Inc()
				pending.d.CursorWindow = &cursor
				continue
			}
			if w.hooks.afterSkipCheck != nil {
				w.hooks.afterSkipCheck(ctx, r.db, win)
			}
			if err := r.workWindow(ctx, page.Entries, evals, win, pending); err != nil {
				if !isDecompressionLimit(err) {
					return false, err
				}
				// The chunk was compressed after the skip check. Nothing of this window
				// committed, so it is a compressed window like any other.
				pending.d.WindowsCompressed++
				w.metrics.WindowsSkipped.WithLabelValues("compressed").Inc()
				pending.d.CursorWindow = &cursor
			}
		}
	}

	// The batch is done: the devices, the roster cursor, the cursor reset, and anything the
	// last windows left pending, in one commit.
	final := pending.delta(r.skipped)
	final.DevicesDone = int64(len(page.Entries))
	var nilWindow *time.Time
	final.CursorWindow = &nilWindow
	if page.Total != nil && job.RosterAfterId == 0 {
		final.DevicesTotal = page.Total
	}
	last := page.NextCursor == 0
	if last {
		refresh := model.BackfillPhaseRefresh
		final.Phase = &refresh
		var nilRefresh *time.Time
		final.RefreshCursor = &nilRefresh
	} else {
		next := page.NextCursor
		final.RosterAfterId = &next
	}
	if err := r.commit(final); err != nil {
		return false, err
	}
	w.metrics.BatchSeconds.Observe(w.now().Sub(started).Seconds())
	if w.hooks.afterCommit != nil {
		if err := w.hooks.afterCommit(ctx); err != nil {
			return false, err
		}
	}
	return false, nil
}

// beyondRetention reports whether a window is within two chunks of the retention horizon, where
// retention may drop its chunk at any moment and inserting would recreate or race it.
func (r *jobRun) beyondRetention(win chunkWindow) bool {
	days := r.w.cfg.RetentionDays
	if days <= 0 {
		return false
	}
	floor := r.w.now().Add(-time.Duration(days) * 24 * time.Hour).Add(2 * r.w.cfg.ChunkInterval)
	return !win.End.After(floor)
}

// isDecompressionLimit recognises the error Timescale gives an insert that would decompress more
// tuples than a transaction may (configuration_limit_exceeded).
func isDecompressionLimit(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "53400"
}

// workWindow fills one chunk window for one device batch in ONE transaction: the derived rows,
// and the progress that accounts for them. It folds the pending skips into the same commit.
func (r *jobRun) workWindow(ctx context.Context, entries []RosterEntry, evals []*model.PlanEvaluator,
	win chunkWindow, pending *acc) error {
	w := r.w
	started := w.now()
	lo, hi := maxTime(win.Start, r.job.RangeFrom), minTime(win.End, r.job.RangeTo)
	start := win.Start
	cursor := &start

	local := newAcc()
	err := r.db.Transaction(func(tx *gorm.DB) error {
		local = newAcc() // a retried transaction function starts over
		for i, e := range entries {
			dlo := maxTime(lo, e.CreatedAt) // a reused token must not inherit an earlier device's readings
			if !dlo.Before(hi) {
				continue
			}
			if err := r.fillDevice(ctx, tx, e, evals[i], dlo, hi, local); err != nil {
				return err
			}
		}
		if w.hooks.beforeCommit != nil {
			if err := w.hooks.beforeCommit(ctx, tx); err != nil {
				return err
			}
		}
		all := newAcc()
		all.add(pending)
		all.add(local)
		d := all.delta(r.skipped)
		d.CursorWindow = &cursor
		return model.CommitBackfillProgress(tx, r.job.Token, r.job.ClaimEpoch, d)
	})
	if err != nil {
		return err
	}
	// Committed: the job's counters now include both.
	all := newAcc()
	all.add(pending)
	all.add(local)
	d := all.delta(r.skipped)
	d.CursorWindow = &cursor
	r.applied(d)
	w.metrics.RowsWritten.Add(float64(local.d.Written))
	w.metrics.Samples.Add(float64(local.d.Samples))
	for reason, n := range local.skips {
		w.metrics.Skipped.WithLabelValues(reason).Add(float64(n))
	}
	*pending = *newAcc()
	w.metrics.BatchSeconds.Observe(w.now().Sub(started).Seconds())
	if w.hooks.afterCommit != nil {
		return w.hooks.afterCommit(ctx)
	}
	return nil
}

// skipAmbiguous is the skip reason of a selected key in a sample that holds one of its inputs
// twice with different values (see evaluate). It is the one reason that is not calcfield's.
const skipAmbiguous = "ambiguous"

// sampleKey identifies a sample: the readings of one event that share one instant.
type sampleKey struct {
	eventId  string
	occurred time.Time
}

// fillDevice reads one device's stored readings in the window, a page at a time, evaluates each
// sample and inserts the derived rows it is missing. Everything is on tx.
func (r *jobRun) fillDevice(ctx context.Context, tx *gorm.DB, e RosterEntry, ev *model.PlanEvaluator,
	lo, hi time.Time, local *acc) error {
	var (
		after   *model.MeasurementEvent // the last row of the previous page
		cur     []model.MeasurementEvent
		curKey  sampleKey
		reqs    []*model.MeasurementEventCreateRequest
		samples int64
	)
	flush := func() error {
		if len(reqs) == 0 {
			return nil
		}
		_, rows, err := model.BuildMeasurementRows(reqs)
		if err != nil {
			return err
		}
		n, err := model.InsertDerivedMeasurementRows(tx, rows)
		if err != nil {
			return err
		}
		local.d.Written += n
		local.d.AlreadyPresent += int64(len(rows)) - n // a live write beat this one to the row
		reqs = reqs[:0]
		return r.w.pacer.wait(ctx, len(rows))
	}
	finish := func() error {
		if len(cur) == 0 {
			return nil
		}
		samples++
		out, err := r.evaluate(ev, e, cur, local)
		if err != nil {
			return err
		}
		reqs = append(reqs, out...)
		cur = cur[:0]
		if len(reqs) >= r.readRows {
			return flush()
		}
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		q := tx.Model(&model.MeasurementEvent{}).
			Select("event_id, occurred_time, event_type, name, payload_id, value").
			Where("device_token = ? AND occurred_time >= ? AND occurred_time < ? AND name IN ?", e.Token, lo, hi, r.names)
		if after != nil {
			q = q.Where("(occurred_time, event_id, name, payload_id) > (?, ?, ?, ?)",
				after.OccurredTime, after.EventId, after.Name, after.PayloadId)
		}
		var page []model.MeasurementEvent
		if err := q.Order("occurred_time ASC, event_id ASC, name ASC, payload_id ASC").
			Limit(r.readRows).Find(&page).Error; err != nil {
			return err
		}
		if err := r.w.pacer.wait(ctx, len(page)); err != nil {
			return err
		}
		for i := range page {
			row := page[i]
			key := sampleKey{eventId: string(row.EventId), occurred: row.OccurredTime}
			if len(cur) > 0 && key != curKey {
				if err := finish(); err != nil {
					return err
				}
			}
			curKey = key
			cur = append(cur, row)
		}
		if len(page) < r.readRows {
			break
		}
		after = &page[len(page)-1]
	}
	if err := finish(); err != nil {
		return err
	}
	if err := flush(); err != nil {
		return err
	}
	local.d.Samples += samples
	return nil
}

// evaluate evaluates one sample and returns the create requests for the selected keys that
// produced a value and are not already stored. A key the sample already holds is counted
// present; one that produced nothing is counted under its skip reason.
func (r *jobRun) evaluate(ev *model.PlanEvaluator, e RosterEntry, sample []model.MeasurementEvent,
	local *acc) ([]*model.MeasurementEventCreateRequest, error) {
	ev.Begin()
	var present []string
	ambiguous := false
	for i := range sample {
		row := &sample[i]
		if r.selected[row.Name] {
			present = append(present, row.Name)
			continue
		}
		// 🔴 ONE INPUT, TWO VALUES AT ONE INSTANT. A message that carried a name twice at an
		// instant with different values stored both rows (a raw row's identity includes its
		// value). Live read the FIRST in message order; storage keeps no message order, and the
		// rows arrive here by payload id, which is a hash. Taking either would write a value
		// live may never have computed, permanently (fill-only), so the sample is skipped and
		// counted instead. Rows of a sample come ordered by name, so a repeat is adjacent.
		if i > 0 && sample[i-1].Name == row.Name && sample[i-1].Value != row.Value {
			ambiguous = true
		}
		ev.Add(row.Name, row.Value)
	}
	if ambiguous {
		for _, k := range r.keys {
			if containsString(present, k) {
				local.d.Computed++
				local.d.AlreadyPresent++
				continue
			}
			local.skips[skipAmbiguous]++
		}
		return nil, nil
	}
	first := &sample[0]
	var out []*model.MeasurementEventCreateRequest
	for _, o := range ev.Eval() {
		if !r.selected[o.Key] {
			continue // evaluated only because a selected key reads it
		}
		if containsString(present, o.Key) {
			local.d.Computed++
			local.d.AlreadyPresent++
			continue
		}
		if !o.OK {
			reason := string(o.Skip)
			if reason == "" {
				reason = string(calcfield.SkipEvalError)
			}
			local.skips[reason]++
			continue
		}
		metric, ok := r.doc.Metric(o.Key)
		if !ok {
			return nil, &failJob{msg: fmt.Sprintf("the frozen plan has no metric %q", o.Key)}
		}
		classifier := uint64(metric.Classifier)
		dataType := metric.DataType
		parent := model.Event{
			EventId:      first.EventId,
			DeviceToken:  e.Token,
			EventType:    first.EventType,
			OccurredTime: first.OccurredTime,
		}
		req, err := model.MeasurementRequestFor(parent, first.OccurredTime, model.MeasurementReading{
			Name: o.Key, Value: o.Value, Classifier: &classifier, Unit: metric.Unit,
			DataType: &dataType, Derived: true,
		})
		if err != nil {
			return nil, err
		}
		local.d.Computed++
		out = append(out, req)
	}
	return out, nil
}

func containsString(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
