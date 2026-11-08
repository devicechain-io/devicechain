// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/integrity"
)

// The HTTP pull channel: a device whose type has commandChannel HTTP_PULL cannot be published to,
// so its commands wait here until it asks for them. Everything below is the platform half of
// that exchange; the HTTP surface (the endpoints a device calls) is event-sources', which
// authenticates the device and calls the operations here under a service token.
//
// The vocabulary of the exchange is the device wire, so it is frozen on release:
//
//   - a PULL claims the commands the device may be sent (the existing claim: QUEUED, HELD or
//     PARKED to SENT, stamping a fresh dispatch nonce) and hands them back;
//   - an ACK is the device saying the command reached it, and records DELIVERY EVIDENCE;
//   - a RESPONSE is the device saying what it did, and settles the command.
//
// 🔴 THE ACK IS ADDITIVE EVIDENCE, NOT A STATUS. There is no DELIVERED status (see
// CommandStatus): SENT and the terminal states keep exactly the meaning they always had, and a
// delivery is recorded beside them in delivered_time and delivery_evidence. Every consumer of
// the lifecycle (SDKs, dashboards, batch retry) is unchanged.

const (
	// DeliveryModeOneway marks a command the device acknowledges and does not answer: the ack
	// settles it as SUCCESSFUL. It is accepted only for a device on the HTTP pull channel, the
	// one channel that has an acknowledgement to settle it on.
	DeliveryModeOneway = "ONEWAY"

	// EvidenceDeviceAck: the device acknowledged receipt on the pull channel.
	EvidenceDeviceAck = "DEVICE_ACK"
	// EvidenceDeviceResponse: the device answered the command. Every transport that carries an
	// answer records it, so a delivered command reads as delivered whatever channel it took.
	EvidenceDeviceResponse = "DEVICE_RESPONSE"

	// PullAckWait is how long a pulled command may go without its acknowledgement before the
	// platform treats the pull as lost: the next pull hands it out again, until the command's
	// attempts are spent. The device acknowledges BEFORE it acts, so an unacknowledged pull is
	// a command the device has not been seen to act on.
	PullAckWait = 60 * time.Second

	// DefaultPullMaxAttempts is how many times one command may be handed out by a pull before
	// the platform gives up on an acknowledgement, when the command names no bound.
	DefaultPullMaxAttempts = 3
	// MaxPullMaxAttempts is the most a command may ask for.
	MaxPullMaxAttempts = 10

	// MaxPullBatch is the most commands one pull hands out.
	MaxPullBatch = 16
)

// PullUnacknowledgedReason is the fixed sentence written to a command the platform stopped
// handing out because no pull of it was ever acknowledged. It is fixed, not interpolated, so it
// identifies this failure exactly.
const PullUnacknowledgedReason = "DELIVERY_UNACKNOWLEDGED: the command was handed to the device's pull " +
	"the most times its attempts allow and was never acknowledged."

const (
	// RejectDeliveryOptionNotApplicable: deliveryMode or maxAttempts was sent for a device that is
	// not on the HTTP pull channel, where neither means anything.
	RejectDeliveryOptionNotApplicable RejectionCode = "DELIVERY_OPTION_NOT_APPLICABLE"

	// RejectDeliveryOptionInvalid: deliveryMode is not a known mode, or maxAttempts is out of range.
	RejectDeliveryOptionInvalid RejectionCode = "DELIVERY_OPTION_INVALID"
)

// PullChannelResolver asks the owner of the device types (device-management) which of a set of
// devices take their commands over HTTP_PULL. An error means nothing was decided.
type PullChannelResolver interface {
	PullDevices(ctx context.Context, deviceTokens []string) (map[string]bool, error)
}

// ErrCommandNotPulled is returned when a device acknowledges or answers a command that was never
// handed to a pull: it is not the pull channel's to settle.
var ErrCommandNotPulled = integrity.NewRefusal(integrity.ClassInvalid,
	"the command was not handed to a pull, so it cannot be acknowledged or answered through the pull channel")

// validateDeliveryOptions checks the request's delivery options in isolation.
func validateDeliveryOptions(mode *string, maxAttempts *int32) error {
	if mode != nil && strings.TrimSpace(*mode) != DeliveryModeOneway {
		return rejected(RejectDeliveryOptionInvalid, "deliveryMode %q is not known; the only mode is %s", *mode, DeliveryModeOneway)
	}
	if maxAttempts != nil && (*maxAttempts < 1 || *maxAttempts > MaxPullMaxAttempts) {
		return rejected(RejectDeliveryOptionInvalid, "maxAttempts %d is out of range; it must be between 1 and %d",
			*maxAttempts, MaxPullMaxAttempts)
	}
	return nil
}

// requirePullChannel refuses delivery options for a device that is not on the pull channel.
// With no resolver wired the question cannot be answered, and an option nothing can honour is
// refused rather than stored.
func (api *Api) requirePullChannel(ctx context.Context, deviceToken string) error {
	if api.PullChannels != nil {
		pull, err := api.PullChannels.PullDevices(ctx, []string{deviceToken})
		if err != nil {
			return unavailableOr(err, "cannot enqueue command: the device's command channel could not be determined")
		}
		if pull[deviceToken] {
			return nil
		}
	}
	return rejected(RejectDeliveryOptionNotApplicable,
		"deliveryMode and maxAttempts apply only to a device whose type has the HTTP_PULL command channel; device %q has not", deviceToken)
}

// PulledCommand is one command a pull handed to a device.
type PulledCommand struct {
	Command *Command
	// Attempt is how many times this command has now been handed out, counting this one.
	Attempt int
}

// pullableWhere selects what a pull may hand out: a command the platform still holds, or one an
// earlier pull handed out and never saw acknowledged for longer than PullAckWait.
func pullableWhere(now time.Time) (string, []any) {
	return "(status IN ? OR (status = ? AND delivery_attempts IS NOT NULL AND delivered_time IS NULL AND sent_time < ?))",
		[]any{claimableStatusStrings(), CommandSent.String(), now.Add(-PullAckWait)}
}

// PullCommands hands a pull-channel device the commands it may be sent, oldest first, claiming
// each with a conditional UPDATE so that two pulls (two replicas, a retry, a client that
// reconnects) can never both be handed one.
//
// 🔴 THE CLAIM IS THE EXISTING ONE. Each command moves to SENT in a single compare-and-set
// predicated on the state the read saw (and on the dispatch nonce, for a re-handed command), the
// same gates MarkSentByToken applies (the maintenance window, the disabled device) apply, and a
// fresh dispatch nonce is stamped. A command lost to another claimer is skipped, not an error.
//
// 🔴 IT FIRST SETTLES THE DEVICE'S OWN EXHAUSTED COMMANDS, so a device that keeps polling sees
// a command whose attempts are spent leave its queue instead of being handed out a fourth time.
func (api *Api) PullCommands(ctx context.Context, deviceToken string, limit int) ([]*PulledCommand, error) {
	if limit <= 0 || limit > MaxPullBatch {
		limit = MaxPullBatch
	}
	now := time.Now()
	if _, err := api.FailUnacknowledgedPulls(ctx, deviceToken, now); err != nil {
		return nil, err
	}
	// 🔴 A CALLED-OFF BATCH'S UNACKNOWLEDGED HAND-OUTS END HERE. Cancellation leaves SENT rows
	// alone, so one a pull handed out and never saw acknowledged would otherwise be offered again
	// after PullAckWait. Only the pull's own, unacknowledged rows: an acknowledged command is one
	// the device holds, and the same brake ConfirmDispatch applies is not this one's to pull back.
	if _, err := api.stopCancelledBatchCommand(ctx,
		"device_token = ? AND delivery_attempts IS NOT NULL AND delivered_time IS NULL", []any{deviceToken}); err != nil {
		return nil, err
	}
	liveSQL, liveArgs := liveWhere(now)
	heldSQL, heldArgs := dispatchableNowWhere(now)
	pullSQL, pullArgs := pullableWhere(now)
	candidates := make([]*Command, 0, limit)
	res := api.RDB.DB(ctx).
		Where("device_token = ?", deviceToken).
		Where(pullSQL, pullArgs...).
		Where("COALESCE(delivery_attempts, 0) < COALESCE(max_attempts, ?)", DefaultPullMaxAttempts).
		Where(liveSQL, liveArgs...).
		Where(heldSQL, heldArgs...).
		Where("(batch_id IS NULL OR batch_id NOT IN (?))", api.cancelledBatchIds(ctx)).
		Where("(tenant_id, device_token) NOT IN (?)", api.disabledDevices(ctx)).
		Order("id ASC").
		Limit(limit).
		Find(&candidates)
	if res.Error != nil {
		return nil, res.Error
	}
	out := make([]*PulledCommand, 0, len(candidates))
	budget := PullResponseBudget
	for _, c := range candidates {
		// 🔴 THE SIZE CAP IS APPLIED BEFORE THE CLAIM, NOT AFTER. A command claimed and then
		// dropped from the answer has spent an attempt and is SENT to a device that never saw it.
		// A command that fits the budget on its own is always reachable: the first one of an
		// answer always fits, so a pull makes progress.
		cost := pullWireCost(c)
		if cost > PullResponseBudget {
			// No pull could carry it, whatever else is in the answer: end it rather than claim
			// it, so its attempts are not spent on an answer the transport will refuse.
			if err := api.failUnpullable(ctx, c, deviceToken); err != nil {
				return nil, err
			}
			continue
		}
		if cost > budget {
			break
		}
		claimed, err := api.claimForPull(ctx, c, deviceToken)
		if err != nil {
			return nil, err
		}
		if claimed == nil {
			continue
		}
		budget -= cost
		attempt := 1
		if claimed.DeliveryAttempts.Valid {
			attempt = int(claimed.DeliveryAttempts.Int32)
		}
		out = append(out, &PulledCommand{Command: claimed, Attempt: attempt})
	}
	return out, nil
}

// PullUnpullableReason is the fixed sentence written to a command too large for any pull to carry.
const PullUnpullableReason = "DELIVERY_TOO_LARGE: the command's payload is too large to be carried in a pull " +
	"response, so it was not handed out."

// failUnpullable ends a command that no pull answer can carry: FAILED, outcome NOT_DELIVERED, with
// attempts untouched, on the state the read saw (so a command another claimer took is left alone).
func (api *Api) failUnpullable(ctx context.Context, c *Command, deviceToken string) error {
	tx := api.RDB.DB(ctx).Model(&Command{Model: gorm.Model{ID: c.ID}}).
		Where("id = ? AND device_token = ? AND status = ? AND delivered_time IS NULL", c.ID, deviceToken, c.Status)
	if c.Status == CommandSent.String() {
		tx = tx.Where("dispatch_nonce = ?", c.DispatchNonce)
	}
	return tx.Updates(terminalUpdate(CommandFailed, OutcomeNotDelivered, map[string]any{
		"error": sql.NullString{String: PullUnpullableReason, Valid: true},
	})).Error
}

// PullResponseBudget bounds the worst-case size, in bytes, of the answer one pull builds. The
// service client that carries the answer to the device-facing listener refuses a body over 1 MiB
// (svcclient.MaxResponseBytes), and a command's payload may be 64 KiB, so 16 of them do not fit.
// The budget leaves a quarter of that for the envelope and for the escaping each layer adds.
const PullResponseBudget = 768 << 10

// pullWireCost is the most a command can add to the answer: its payload, as READ BACK from the
// database, counted at the widest escape JSON gives a byte (six), plus a fixed allowance for the
// name, token, timestamps and field names. It is measured on the read-back text because the
// stored form can be larger than the 64 KiB accepted on input (a jsonb column writes 1e300 out in
// full). A payload as accepted costs at most 64 KiB x 6 = 384 KiB, inside the budget on its own.
func pullWireCost(c *Command) int {
	cost := 1024 + len(c.Name) + len(c.Token)
	if c.Payload != nil {
		cost += 6 * len(*c.Payload)
	}
	return cost
}

// claimForPull is the conditional claim of one candidate the read saw. It returns the claimed row,
// or nil when the claim was lost: to another claimer, or because the row stopped being claimable
// between the read and now.
//
// 🔴 EVERY PREDICATE THE SELECTION APPLIES IS APPLIED AGAIN HERE, because the read and the write
// are two statements. A command that expires, or whose batch is called off, in between must not be
// handed to the device: the claim is the last point at which it can still be refused.
func (api *Api) claimForPull(ctx context.Context, c *Command, deviceToken string) (*Command, error) {
	// The time of THIS statement, not of the pull's start: the read and the claims before this one
	// take time, and a command that expired during them must not be handed out.
	now := time.Now()
	liveSQL, liveArgs := liveWhere(now)
	heldSQL, heldArgs := dispatchableNowWhere(now)
	claim := api.RDB.DB(ctx).Model(&Command{Model: gorm.Model{ID: c.ID}}).
		Where("id = ? AND device_token = ?", c.ID, deviceToken).
		Where("COALESCE(delivery_attempts, 0) < COALESCE(max_attempts, ?)", DefaultPullMaxAttempts).
		Where(liveSQL, liveArgs...).
		Where(heldSQL, heldArgs...).
		Where("(batch_id IS NULL OR batch_id NOT IN (?))", api.cancelledBatchIds(ctx)).
		Where("(tenant_id, device_token) NOT IN (?)", api.disabledDevices(ctx))
	if c.Status == CommandSent.String() {
		// A re-handed command: only the dispatch the read saw may be replaced.
		claim = claim.Where("status = ? AND dispatch_nonce = ? AND delivered_time IS NULL",
			CommandSent.String(), c.DispatchNonce)
	} else {
		claim = claim.Where("status = ?", c.Status)
	}
	upd := claim.Updates(map[string]any{
		"status":            CommandSent.String(),
		"sent_time":         sql.NullTime{Time: now, Valid: true},
		"dispatch_nonce":    sql.NullString{String: newDispatchNonce(), Valid: true},
		"delivery_attempts": gorm.Expr("COALESCE(delivery_attempts, 0) + 1"),
	})
	if upd.Error != nil {
		return nil, upd.Error
	}
	if upd.RowsAffected == 0 {
		return nil, nil
	}
	return api.loadCommand(ctx, c.ID)
}

// AcknowledgeDelivery records the device's acknowledgement of a command a pull handed it. It
// reports whether the acknowledgement stands: true for the call that recorded it and for any
// repeat of it (an acknowledgement is retried until it is answered, so it is idempotent), false
// for a command that is not awaiting one.
//
// A ONEWAY command is settled by its acknowledgement: SUCCESSFUL, outcome SUCCEEDED.
//
// 🔴 A LATE ACKNOWLEDGEMENT DOES NOT RESURRECT A COMMAND. Once the platform has failed a command
// as unacknowledged, or expired or cancelled it, the row is terminal and the acknowledgement
// matches nothing: the device is told so (false), and delivered_time stays what it was.
func (api *Api) AcknowledgeDelivery(ctx context.Context, token, deviceToken string) (bool, error) {
	found, err := api.pulledCommand(ctx, token, deviceToken)
	if err != nil {
		return false, err
	}
	if found.DeliveredTime.Valid {
		return true, nil
	}
	if found.Status != CommandSent.String() {
		return false, nil
	}
	now := time.Now()
	updates := map[string]any{
		"delivered_time":    sql.NullTime{Time: now, Valid: true},
		"delivery_evidence": sql.NullString{String: EvidenceDeviceAck, Valid: true},
	}
	if found.DeliveryMode.Valid && found.DeliveryMode.String == DeliveryModeOneway {
		updates = terminalUpdate(CommandSuccessful, OutcomeSucceeded, updates)
		updates["responded_time"] = sql.NullTime{Time: now, Valid: true}
	}
	res := api.RDB.DB(ctx).Model(&Command{Model: gorm.Model{ID: found.ID}}).
		Where("id = ? AND device_token = ? AND status = ? AND delivered_time IS NULL AND dispatch_nonce = ?",
			found.ID, deviceToken, CommandSent.String(), found.DispatchNonce).
		Updates(updates)
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected == 0 {
		// Moved between the read and the write: report where it is now.
		current, err := api.loadCommand(ctx, found.ID)
		if err != nil {
			return false, err
		}
		return current.DeliveredTime.Valid, nil
	}
	return true, nil
}

// RecordPulledResponse records what a pull-channel device did with a command, settling it
// through MarkResponse and the dispatch nonce the row currently carries. The device never holds
// a nonce: the platform handed it the command, and the pull is what proves it received it.
func (api *Api) RecordPulledResponse(ctx context.Context, token, deviceToken string, success bool,
	payload *string, errMsg *string) (*Command, error) {
	found, err := api.pulledCommand(ctx, token, deviceToken)
	if err != nil {
		return nil, err
	}
	if !found.DispatchNonce.Valid {
		return nil, ErrCommandNotPulled
	}
	return api.MarkResponse(ctx, token, deviceToken, found.DispatchNonce.String, success, ReachAnswered, payload, errMsg)
}

// pulledCommand reads a command the named device owns and a pull handed out. A command that does
// not exist, or belongs to another device, is gorm.ErrRecordNotFound for both: the caller learns
// nothing about another device's command.
func (api *Api) pulledCommand(ctx context.Context, token, deviceToken string) (*Command, error) {
	matches, err := api.CommandsByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 || matches[0].DeviceToken != deviceToken {
		return nil, gorm.ErrRecordNotFound
	}
	if !matches[0].DeliveryAttempts.Valid {
		return nil, ErrCommandNotPulled
	}
	return matches[0], nil
}

// FailUnacknowledgedPulls ends the commands a pull handed out as many times as their attempts
// allow and never saw acknowledged for longer than PullAckWait: FAILED, outcome NOT_DELIVERED,
// delivered_time left NULL. deviceToken narrows it to one device (the pull's own), empty
// covers every device (the periodic sweep, under a system context). It reports how many it ended.
//
// 🔴 NOT_DELIVERED IS A CLAIM ABOUT THE WIRE CONTRACT, AND THE CONTRACT IS WHAT MAKES IT TRUE. The
// device acknowledges a command BEFORE it acts on it, so a command that was handed out its full
// allowance and never acknowledged is one the device has not been seen to act on, and sending it
// again is safe in the sense NOT_DELIVERED promises. A device that acts first and acknowledges
// after is outside that contract, and for it this outcome can be wrong: its commands would be
// better sent ONEWAY with the response endpoint doing the settling.
func (api *Api) FailUnacknowledgedPulls(ctx context.Context, deviceToken string, now time.Time) (int64, error) {
	tx := api.RDB.DB(ctx).Model(&Command{}).
		Where("status = ? AND delivery_attempts IS NOT NULL AND delivered_time IS NULL AND sent_time < ?",
			CommandSent.String(), now.Add(-PullAckWait)).
		Where("COALESCE(delivery_attempts, 0) >= COALESCE(max_attempts, ?)", DefaultPullMaxAttempts)
	if deviceToken != "" {
		tx = tx.Where("device_token = ?", deviceToken)
	}
	res := tx.Updates(terminalUpdate(CommandFailed, OutcomeNotDelivered, map[string]any{
		"error": sql.NullString{String: PullUnacknowledgedReason, Valid: true},
	}))
	if res.Error != nil {
		return 0, fmt.Errorf("failing unacknowledged pulls: %w", res.Error)
	}
	return res.RowsAffected, nil
}
