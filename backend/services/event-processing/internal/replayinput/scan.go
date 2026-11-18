// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package replayinput shares the admission contract of recorded-input consumers.
package replayinput

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
)

const DefaultMaxScan = 500_000
const DefaultMaxRead = 5_000_000

type TimeRange struct{ Start, End time.Time }
type Opener interface {
	NewReplayReaderFromTime(string, time.Time) (messaging.ReplayReader, time.Time, error)
}
type Admission struct {
	Tenant           string
	AdmitVersion     func(string) bool
	Devices          map[string]bool
	Span             TimeRange
	UpperSeq         uint64
	MaxScan, MaxRead int
	AppliedGuard     bool
}
type Counts struct {
	Read, Scanned, Corrupt, Disabled int
	FirstSeq, LastSeq                uint64
}
type Stop string

const (
	StopReadCap    Stop = "READ_CAP"
	StopScanCap    Stop = "SCAN_CAP"
	StopDeadline   Stop = "DEADLINE"
	StopUpperBound Stop = "UPPER_BOUND"
)

// Scan leaves closing the reader to its caller. Sequence counts include filtered messages
// and the delivered message that trips a cap or the recorded upper bound.
func Scan(ctx context.Context, reader messaging.ReplayReader, adm Admission, visit func(messaging.Message, *resolvedevent.ResolvedEvent, bool)) (c Counts, stop Stop, err error) {
	maxRead, maxScan := adm.MaxRead, adm.MaxScan
	if maxRead <= 0 {
		maxRead = DefaultMaxRead
	}
	if maxScan <= 0 {
		maxScan = DefaultMaxScan
	}
	var guard *runtime.AppliedGuard
	if adm.AppliedGuard {
		guard = runtime.NewAppliedGuard(0, 0)
	}
	defer func() {
		if e := ctx.Err(); e != nil {
			if errors.Is(e, context.DeadlineExceeded) {
				if stop == "" {
					stop = StopDeadline
				}
			} else {
				err = e
			}
		}
	}()
	for {
		if e := ctx.Err(); e != nil {
			if errors.Is(e, context.DeadlineExceeded) {
				return c, StopDeadline, nil
			}
			return c, "", e
		}
		msg, e := reader.Read(ctx)
		if errors.Is(e, io.EOF) {
			return c, "", nil
		}
		if errors.Is(e, context.DeadlineExceeded) {
			return c, StopDeadline, nil
		}
		if e != nil {
			return c, "", e
		}
		if c.FirstSeq == 0 {
			c.FirstSeq = msg.StreamSeq
		}
		c.LastSeq = msg.StreamSeq
		if adm.UpperSeq > 0 && msg.StreamSeq > adm.UpperSeq {
			return c, StopUpperBound, nil
		}
		c.Read++
		if c.Read > maxRead {
			return c, StopReadCap, nil
		}
		tenant, ok := messaging.ParseTenantFromSubject(msg.Subject)
		if !ok || tenant != adm.Tenant {
			continue
		}
		values, enveloped := [][]byte{msg.Value}, msg.IsEnvelope()
		if enveloped {
			units, _, e := messaging.SplitEnvelope(msg, tenant, func(string) {}, nil)
			if e != nil {
				c.Corrupt++
				continue
			}
			values = make([][]byte, len(units))
			for i, u := range units {
				values[i] = u.Msg.Value
			}
		}
		for _, v := range values {
			ev, e := resolvedevent.UnmarshalResolvedEvent(v)
			if e != nil {
				c.Corrupt++
				continue
			}
			if ev.SourceDeviceDisabled {
				c.Disabled++
				continue
			}
			if adm.AdmitVersion != nil && !adm.AdmitVersion(ev.ProfileVersionToken) {
				continue
			}
			if adm.Devices != nil && !adm.Devices[ev.SourceDeviceToken] {
				continue
			}
			if guard != nil {
				if id, e := runtime.EventIdentity(adm.Tenant, ev); e == nil && !guard.Admit(msg.AppendTime, id) {
					continue
				}
			}
			first, last := occurredSpan(ev)
			if last.Before(adm.Span.Start) || first.After(adm.Span.End) {
				continue
			}
			if c.Scanned >= maxScan {
				return c, StopScanCap, nil
			}
			c.Scanned++
			visit(msg, ev, enveloped)
		}
	}
}
func occurredSpan(event *resolvedevent.ResolvedEvent) (time.Time, time.Time) {
	first, last := event.OccurredTime, event.OccurredTime
	observe := func(at time.Time) {
		if at.Before(first) {
			first = at
		}
		if at.After(last) {
			last = at
		}
	}
	switch payload := event.Payload.(type) {
	case *resolvedevent.ResolvedMeasurementsPayload:
		for _, entry := range payload.Entries {
			observe(entry.OccurredTime)
		}
	case *resolvedevent.ResolvedLocationsPayload:
		for _, entry := range payload.Entries {
			observe(entry.OccurredTime)
		}
	case *resolvedevent.ResolvedAlertsPayload:
		for _, entry := range payload.Entries {
			observe(entry.OccurredTime)
		}
	}
	return first, last
}
