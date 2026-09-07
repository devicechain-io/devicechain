// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// CommandNudger is told that a device has just been handed a command, so that device's
// queued backlog can be dispatched NOW instead of at the delivery sweep's next tick.
//
// 🔑 WHY THE SEAM IS THIS NARROW — A DEVICE, NOT A COMMAND. Per-device ordering is a
// delivery guarantee (see DrainableCommands: a firmware update is a sequence whose order
// IS its meaning), so the unit a dispatcher may act on is a device's backlog, never one
// row out of the middle of it. Handing over the row would invite exactly the caller that
// dispatches its own command and steps over an older one.
//
// It must not block and must not report failure. The caller is CreateCommand, on the hot
// enqueue path, and there is nothing useful it could do with an error: the delivery sweep
// is the net underneath every nudge, so the worst outcome of a nudge that never happens
// is the latency the platform had before this existed.
//
// Nil on the Api means the nudge is OFF, which is a supported configuration and not a
// misconfiguration: every command then waits for the sweep, exactly as it did before.
type CommandNudger interface {
	NudgeDevice(tenant, deviceToken string)
}

// NudgeProbeLimit is how many of a device's queued commands the nudge reads.
//
// 🔴 TWO IS THE WHOLE ANSWER, AND IT IS TWO RATHER THAN ONE FOR A REASON. The nudge
// dispatches only when the new row is the device's ONLY queued command; if an older
// QUEUED row exists it stands down and leaves the device to the sweep. Distinguishing
// "exactly one" from "more than one" needs the second row to be readable — a LIMIT 1
// read cannot tell the two apart, and would answer "exactly one" for a device with a
// hundred queued commands, which is the precise case the stand-down exists to refuse.
//
// Reading no further than that is deliberate too: nothing downstream looks at rows beyond
// the second, so a larger probe would only make the refusal path more expensive on the
// devices that trigger it most.
const NudgeProbeLimit = 2

// QueuedCommandsForDevice reads the front of ONE device's QUEUED backlog, oldest first.
//
// It is the dispatch nudge's read, and it answers two questions with one query: which
// command to dispatch (the first row) and whether the nudge may dispatch at all (whether
// there is a second). Bounded by limit, because the caller only ever needs the front —
// see NudgeProbeLimit.
//
// 🔴 THE STATUS SET IS sweepableStatusStrings(), SHARED WITH THE SWEEP RATHER THAN
// RESTATED. The nudge is an ACCELERATOR of the sweep, not a second delivery policy: if the
// two ever selected different sets, the nudge would either dispatch rows the sweep has
// decided it does not own (HELD belongs to the presence gate and the wake, not to this) or
// stand down on rows the sweep would have sent. A literal 'QUEUED' here would compile,
// pass, and drift the first time that set changes.
//
// 🔴 NO EXPIRY FILTER, FOR THE SAME REASON. PendingCommands applies none either — expiry
// is ExpireStale's job, once, on the sweep's own tick — so filtering here would make the
// nudge dispatch a strictly different set from the sweep. The visible consequence is
// benign and worth stating: an expired-but-not-yet-swept QUEUED row still counts as a
// second queued command, so the nudge stands down. Standing down is always safe.
//
// It builds on api.RDB.DB(ctx) exactly as PendingCommands and DrainableCommands do, so
// the dc:tenant_query scope callback injects the tenant predicate. 🔴 deviceToken is a
// filter INSIDE that fence and never a substitute for it: device tokens are unique per
// tenant, not per instance.
//
// limit: absent, zero or negative means NudgeProbeLimit; anything above rdb.MaxPageSize is
// clamped to it, matching DrainableCommands.
func (api *Api) QueuedCommandsForDevice(ctx context.Context, deviceToken string, limit int) ([]*Command, error) {
	if limit <= 0 {
		limit = NudgeProbeLimit
	}
	if limit > rdb.MaxPageSize {
		limit = rdb.MaxPageSize
	}
	found := make([]*Command, 0, limit)
	result := api.RDB.DB(ctx).
		Where("device_token = ?", deviceToken).
		Where("status IN ?", sweepableStatusStrings()).
		Order("id ASC").
		Limit(limit).
		Find(&found)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// nudgeDispatch asks the delivery path to consider this device now rather than on the
// sweep's next tick.
//
// 🔴 IT IS CALLED ONLY WHERE A ROW WAS ACTUALLY INSERTED. A replay — the same token
// arriving twice, which REACT's at-least-once send-command makes a NORMAL path rather than
// an edge case — creates nothing, so nudging on it would put one command's dispatch behind
// an unbounded number of duplicate probes for as long as the retries continue. The insert
// that created the row already nudged; if that nudge was dropped, the sweep is what covers
// it, which is the same net every other drop leans on.
//
// 🔑 THE TENANT COMES FROM THE CONTEXT, NOT FROM THE ROW. Both are correct here — the
// create callback stamps the row from the same context — but the context is the value this
// call was authorized under, and reading it here means a future caller that somehow
// produced a row with another tenant's id could not use this to have that tenant's device
// dispatched. No tenant means no nudge: the read on the far side would fail closed anyway,
// and dropping it silently costs latency and nothing else.
func (api *Api) nudgeDispatch(ctx context.Context, created *Command) {
	if api.Nudger == nil || created == nil {
		return
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return
	}
	api.Nudger.NudgeDevice(tenant, created.DeviceToken)
}
