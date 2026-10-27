// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/maintwindow"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// decodeSlots reads a window's slots document STRICTLY: an unrecognised key is refused rather
// than dropped, so a misspelt field cannot create a window that quietly means something else.
func decodeSlots(raw string) ([]maintwindow.Slot, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("%w: slots is required", maintwindow.ErrInvalid)
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	var slots []maintwindow.Slot
	if err := dec.Decode(&slots); err != nil {
		return nil, fmt.Errorf("%w: slots is not a JSON array of {day, start, durationMinutes}: %v",
			maintwindow.ErrInvalid, err)
	}
	if dec.More() {
		return nil, fmt.Errorf("%w: slots has trailing content after the array", maintwindow.ErrInvalid)
	}
	return slots, nil
}

// validateWindow checks a (zone, slots) pair by building the schedule from it. Using the
// calculator itself as the validator means a window that saves is a window that computes.
func validateWindow(timezone, slotsJSON string) (datatypes.JSON, error) {
	slots, err := decodeSlots(slotsJSON)
	if err != nil {
		return nil, err
	}
	if _, err := maintwindow.New(timezone, slots); err != nil {
		return nil, err
	}
	// Re-encode from the decoded value so what is stored is the canonical form, whatever
	// whitespace or key order the caller used.
	canonical, err := json.Marshal(slots)
	if err != nil {
		return nil, err
	}
	return datatypes.JSON(canonical), nil
}

// CreateMaintenanceWindow creates a window.
func (api *Api) CreateMaintenanceWindow(ctx context.Context,
	request *MaintenanceWindowCreateRequest) (*MaintenanceWindow, error) {
	slots, err := validateWindow(request.Timezone, request.Slots)
	if err != nil {
		return nil, err
	}
	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata)
	if err != nil {
		return nil, err
	}
	created := &MaintenanceWindow{
		TokenReference: rdb.TokenReference{Token: request.Token},
		NamedEntity: rdb.NamedEntity{
			Name:        rdb.NullStrOf(request.Name),
			Description: rdb.NullStrOf(request.Description),
		},
		MetadataEntity: rdb.MetadataEntity{Metadata: metadataJSON},
		Timezone:       strings.TrimSpace(request.Timezone),
		Slots:          slots,
	}
	if err := api.RDB.DB(ctx).Create(created).Error; err != nil {
		return nil, err
	}
	return created, nil
}

// UpdateMaintenanceWindow applies a PARTIAL update. The (zone, slots) pair is validated as
// the pair it will be after the update, so changing only the zone cannot leave slots that
// no longer make sense.
//
// Command-delivery learns of the edit by polling (see its window re-stamp pass): rows waiting
// on this window are moved to the new schedule's next opening within one pass.
func (api *Api) UpdateMaintenanceWindow(ctx context.Context, token string,
	request *MaintenanceWindowUpdateRequest) (*MaintenanceWindow, error) {
	matches, err := api.MaintenanceWindowsByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	updated := matches[0]

	timezone, err := request.Timezone.ApplyToRequired("timezone", updated.Timezone)
	if err != nil {
		return nil, err
	}
	slotsJSON, err := request.Slots.ApplyToRequired("slots", string(updated.Slots))
	if err != nil {
		return nil, err
	}
	slots, err := validateWindow(timezone, slotsJSON)
	if err != nil {
		return nil, err
	}
	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata.ApplyTo(dcgraphql.MetadataStr(updated.Metadata)))
	if err != nil {
		return nil, err
	}
	updated.Timezone = strings.TrimSpace(timezone)
	updated.Slots = slots
	updated.Metadata = metadataJSON
	updated.Name = request.Name.ApplyToNullString(updated.Name)
	updated.Description = request.Description.ApplyToNullString(updated.Description)
	if err := api.RDB.DB(ctx).Save(updated).Error; err != nil {
		return nil, err
	}
	return updated, nil
}

// MaintenanceWindowsById gets windows by id.
func (api *Api) MaintenanceWindowsById(ctx context.Context, ids []uint) ([]*MaintenanceWindow, error) {
	return rdb.FindByIds[MaintenanceWindow](api.RDB.DB(ctx), ids)
}

// MaintenanceWindowsByToken gets windows by token.
func (api *Api) MaintenanceWindowsByToken(ctx context.Context, tokens []string) ([]*MaintenanceWindow, error) {
	found := make([]*MaintenanceWindow, 0)
	if err := rdb.FindByKeys(api.RDB.DB(ctx), &found, "token", tokens); err != nil {
		return nil, err
	}
	return found, nil
}

// MaintenanceWindows lists windows.
func (api *Api) MaintenanceWindows(ctx context.Context,
	criteria MaintenanceWindowSearchCriteria) (*MaintenanceWindowSearchResults, error) {
	results := make([]MaintenanceWindow, 0)
	db, pag := api.RDB.ListOf(ctx, &MaintenanceWindow{}, nil, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &MaintenanceWindowSearchResults{Results: results, Pagination: pag}, nil
}

// DeleteMaintenanceWindow deletes a window. Refused while any device type is assigned to it:
// deleting it would silently un-gate every disruptive command of those types.
func (api *Api) DeleteMaintenanceWindow(ctx context.Context, token string) (bool, error) {
	return deleteParentType(ctx, api, (*Api).MaintenanceWindowsByToken,
		func(m *MaintenanceWindow) uint { return m.ID }, &MaintenanceWindow{}, token,
		&DeviceType{}, "maintenance_window_id")
}

// resolveMaintenanceWindowId maps an optional window token to its id, mirroring
// resolveProfileId: nil or blank clears the assignment, an unknown token is refused.
func (api *Api) resolveMaintenanceWindowId(ctx context.Context, token *string) (*uint, error) {
	if token == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*token)
	if trimmed == "" {
		return nil, nil
	}
	matches, err := api.MaintenanceWindowsByToken(ctx, []string{trimmed})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("%w: maintenance window %q", gorm.ErrRecordNotFound, trimmed)
	}
	id := matches[0].ID
	return &id, nil
}

// RejectWindowAfterExpiry: the command is held for the device type's maintenance window and
// the next opening comes after the command's own expiry, so it could only expire unsent.
// It is refused at enqueue rather than accepted to lapse. Permanent for this expiry: a later
// expiresAt, or a window that opens sooner, would be accepted.
const RejectWindowAfterExpiry EnqueueRejectionCode = "WINDOW_AFTER_EXPIRY"

// CommandDispatchHold binds a device's command to one opening of its device type's
// maintenance window WindowToken: it may START in [DispatchAfter, DispatchUntil) and at no
// other time. When the window is open at enqueue DispatchAfter is already past, so the command
// may go at once; DispatchUntil is still the close of that opening, because a command that is
// not claimed before the window closes (its device is offline, or the queue is long) must not
// be sent whenever its device returns.
type CommandDispatchHold struct {
	DeviceToken   string
	WindowToken   string
	DispatchAfter time.Time
	DispatchUntil time.Time
}

// CommandDispatchSchedule is the dispatch-time half of the window gate, asked at enqueue.
// For each device it answers one of three things: nothing (the command is not gated — it may
// dispatch at any time), a HOLD (only inside the opening [DispatchAfter, DispatchUntil), which
// may be the one open now), or a REFUSAL (WINDOW_AFTER_EXPIRY — the next opening is after the
// command expires).
//
// 🔴 A GATED COMMAND IS ALWAYS HELD, EVEN WHEN ITS WINDOW IS OPEN NOW. Answering "nothing" for
// an open window would leave the command with no window at all: a device that is offline at
// enqueue and returns after the close would be sent a disruptive command outside any window,
// and the re-stamp pass, which finds rows by their window, would never see it.
//
// Like the enqueue gate it is a function of the device TYPE: the window and the command's
// impact are resolved once per distinct type, so a ten-thousand-device batch over three
// types reads three windows.
//
// Devices that do not exist or are disabled are skipped, not reported: the enqueue gate has
// already decided those, and answering here too would make one refusal say two things.
//
// An error means the schedule could not be determined (a database failure, or a stored
// window that no longer validates). The caller must fail closed on it — it must never be
// read as "no hold".
//
// expiresAt is optional; nil means the command never expires and can always wait.
//
// 🔑 UNGATED IS THE THIRD ANSWER SPELLED OUT, and it exists for the re-stamp pass rather than
// for enqueue. A device that exists, is enabled and whose type does not gate this command (no
// window, or the command is not disruptive) is listed there. Enqueue could treat "not held, not
// refused" as ungated, because a skipped device never reaches the queue. The re-stamp pass
// cannot: it asks about commands ALREADY waiting, and a disabled or deleted device is skipped
// too — reading that silence as "the type has no window now" would release a disabled device's
// held disruptive commands to dispatch outside any window the moment it is enabled. Only a
// device named here may have its hold released.
func (api *Api) CommandDispatchSchedule(ctx context.Context, deviceTokens []string, commandKey string,
	expiresAt *time.Time, at time.Time) ([]*CommandDispatchHold, []*BatchEnqueueRefusal, []string, error) {
	schedule, err := api.DispatchScheduleFor(ctx, deviceTokens, commandKey, expiresAt, at, false)
	if err != nil {
		return nil, nil, nil, err
	}
	return schedule.Holds, schedule.Refusals, schedule.Ungated, nil
}

// DispatchSchedule is the whole answer of DispatchScheduleFor: every enabled, existing device
// asked about is in exactly one of the four lists.
type DispatchSchedule struct {
	Holds    []*CommandDispatchHold
	Refusals []*BatchEnqueueRefusal
	// Ungated: the device's type does not gate the command (see DispatchScheduleFor for how
	// alreadyHeld changes what that means).
	Ungated []string
	// Undefined: the profile version the device's type is on now does not define the command.
	Undefined []string
	// Impacts is the impact (NORMAL, DISRUPTIVE or DESTRUCTIVE) the device's type's current
	// profile version gives the command, for every device whose type DEFINES it. It is the
	// classification the window gate decided on, exposed so a transport adapter that gates on
	// impact itself reads the same answer rather than re-deriving it.
	Impacts []*CommandDispatchImpact
}

// CommandDispatchImpact is one device's command impact, as decided with the dispatch schedule.
type CommandDispatchImpact struct {
	DeviceToken string
	Impact      string
}

// DispatchScheduleFor is CommandDispatchSchedule with the fourth list, UNDEFINED, and the mode
// the re-stamp pass asks in.
//
// alreadyHeld=false is the enqueue question: is THIS command, as the profile classifies it now,
// gated? A NORMAL command is ungated.
//
// alreadyHeld=true is the re-stamp question about a command that is ALREADY waiting for a
// window: which window must it keep waiting for? The answer then follows the device type's
// window ALONE and ignores the command's current impact. 🔴 THAT IS THE POINT OF THE MODE. A
// waiting disruptive command is released only when an operator changes the WINDOW (re-points
// the type, or unassigns its window); a new profile version that marks the command NORMAL is
// a change to how FUTURE commands are classified, and must not send one that was queued as
// disruptive outside its window. Under this mode a device is ungated only when its type has no
// window at all.
//
// In both modes a command the type's current profile version does not define at all (dropped
// from the vocabulary, or the type has no profile now) is UNDEFINED rather than ungated: the
// re-stamp pass fails such a command and never sends it, and enqueue's vocabulary gate has
// already refused it.
func (api *Api) DispatchScheduleFor(ctx context.Context, deviceTokens []string, commandKey string,
	expiresAt *time.Time, at time.Time, alreadyHeld bool) (*DispatchSchedule, error) {
	holds := make([]*CommandDispatchHold, 0)
	refusals := make([]*BatchEnqueueRefusal, 0)
	ungated := make([]string, 0)
	undefined := make([]string, 0)
	impacts := make([]*CommandDispatchImpact, 0)
	ordered := dedupeTokens(deviceTokens)

	verdicts := make(map[uint]typeWindowDecision)
	windows := make(map[uint]*MaintenanceWindow)

	for start := 0; start < len(ordered); start += deviceLookupChunk {
		end := start + deviceLookupChunk
		if end > len(ordered) {
			end = len(ordered)
		}
		found, err := api.DevicesByToken(ctx, ordered[start:end])
		if err != nil {
			return nil, err
		}
		byToken := make(map[string]*Device, len(found))
		for _, d := range found {
			if d != nil {
				byToken[d.Token] = d
			}
		}
		for _, token := range ordered[start:end] {
			device, ok := byToken[token]
			if !ok || device.IsDisabled() {
				continue
			}
			v, resolved := verdicts[device.DeviceTypeId]
			if !resolved {
				var err error
				v, err = api.typeWindowVerdict(ctx, device.DeviceTypeId, commandKey, at, windows, alreadyHeld)
				if err != nil {
					return nil, err
				}
				verdicts[device.DeviceTypeId] = v
			}
			if !v.defined {
				undefined = append(undefined, token)
				continue
			}
			impacts = append(impacts, &CommandDispatchImpact{DeviceToken: token, Impact: v.impact})
			if !v.gated {
				ungated = append(ungated, token)
				continue
			}
			if expiresAt != nil && v.after.After(at) && v.after.After(*expiresAt) {
				refusals = append(refusals, &BatchEnqueueRefusal{
					DeviceToken: token,
					Code:        RejectWindowAfterExpiry,
					Reason: fmt.Sprintf("command %q waits for the maintenance window of device %q, which next "+
						"opens at %s — after the command expires at %s; extend expiresAt or send it later",
						commandKey, token, v.after.UTC().Format(time.RFC3339), expiresAt.UTC().Format(time.RFC3339)),
				})
				continue
			}
			holds = append(holds, &CommandDispatchHold{
				DeviceToken: token, WindowToken: v.windowToken, DispatchAfter: v.after, DispatchUntil: v.until,
			})
		}
	}
	return &DispatchSchedule{Holds: holds, Refusals: refusals, Ungated: ungated, Undefined: undefined,
		Impacts: impacts}, nil
}

// typeWindowDecision is the part of a dispatch schedule that depends only on the device TYPE.
type typeWindowDecision struct {
	// defined: the profile version the type is on now defines the command.
	defined bool
	// gated: the command is DISRUPTIVE or DESTRUCTIVE (or, asked with alreadyHeld, defined at
	// all) on a type that has a window.
	gated bool
	// impact is the definition's effective impact, set whenever defined is.
	impact      string
	windowToken string
	// [after, until) is the opening the command must start in: the one open now (after is
	// then not later than the instant asked about), or the next.
	after time.Time
	until time.Time
}

// typeWindowVerdict resolves one device type's decision. windows memoizes the windows loaded
// so far across the types of one call. alreadyHeld is DispatchScheduleFor's mode: the command's
// current impact then does not decide whether it stays on the type's window.
func (api *Api) typeWindowVerdict(ctx context.Context, deviceTypeId uint, commandKey string, at time.Time,
	windows map[uint]*MaintenanceWindow, alreadyHeld bool) (typeWindowDecision, error) {
	type result = typeWindowDecision
	types, err := api.DeviceTypesById(ctx, []uint{deviceTypeId})
	if err != nil {
		return result{}, err
	}
	if len(types) == 0 {
		// The device exists and names this type, so a type that cannot be found is a breach,
		// not an answer: reading it as "defines nothing" would FAIL every waiting command of
		// its devices.
		return result{}, fmt.Errorf("device type %d cannot be found; its command vocabulary is not decided",
			deviceTypeId)
	}
	definitions, err := api.decidedCommandDefinitions(ctx, types[0])
	if err != nil {
		return result{}, err
	}
	var matched *CommandDefinition
	for _, def := range definitions {
		if def != nil && def.CommandKey == commandKey {
			matched = def
			break
		}
	}
	if matched == nil {
		return result{}, nil // not defined by the profile version the type is on now
	}
	impact := matched.EffectiveImpact()
	if !alreadyHeld && !matched.WindowGated() {
		return result{defined: true, impact: impact}, nil
	}
	if types[0].MaintenanceWindowId == nil {
		return result{defined: true, impact: impact}, nil // gated command, but the type has no window: nothing to wait for
	}
	windowId := *types[0].MaintenanceWindowId
	window, cached := windows[windowId]
	if !cached {
		found, err := api.MaintenanceWindowsById(ctx, []uint{windowId})
		if err != nil {
			return result{}, err
		}
		if len(found) == 0 {
			return result{}, fmt.Errorf("device type %d references maintenance window %d, which does not exist",
				deviceTypeId, windowId)
		}
		window = found[0]
		windows[windowId] = window
	}
	schedule, err := window.Schedule()
	if err != nil {
		return result{}, fmt.Errorf("maintenance window %q cannot be evaluated: %w", window.Token, err)
	}
	iv := schedule.Next(at)
	return result{defined: true, impact: impact, gated: true, windowToken: window.Token,
		after: iv.Start, until: iv.End}, nil
}

// decidedCommandDefinitions is the command vocabulary of the profile version a device type is
// on now, for a caller that ACTS on "not defined".
//
// 🔴 IT IS NOT CommandDefinitionsByDeviceType, AND THE DIFFERENCE IS DATA LOSS. That lookup
// serves ingest and the enqueue vocabulary gate, where an unreadable vocabulary failing to
// "nothing defined" refuses a command or ignores a reading: safe directions. Here "not defined"
// FAILS every command the re-stamp pass finds waiting for the type's devices, so only a
// definitive answer may produce it: the type has no profile, or its profile has never been
// published. A profile row that is missing, or an active-version pointer naming a version row
// that is missing, is an invariant breach and an error, never an empty vocabulary. (The same
// stance activeConfigurationDeclaration takes, for the same reason.)
func (api *Api) decidedCommandDefinitions(ctx context.Context, deviceType *DeviceType) ([]*CommandDefinition, error) {
	if deviceType.ProfileId == nil {
		return []*CommandDefinition{}, nil
	}
	profiles, err := api.DeviceProfilesById(ctx, []uint{*deviceType.ProfileId})
	if err != nil {
		return nil, err
	}
	if len(profiles) == 0 {
		return nil, fmt.Errorf("device type %q references device profile %d, which cannot be found",
			deviceType.Token, *deviceType.ProfileId)
	}
	if !profiles[0].ActiveVersion.Valid {
		return []*CommandDefinition{}, nil
	}
	var versions []DeviceProfileVersion
	if err := api.RDB.DB(ctx).Where("device_profile_id = ? AND version = ?", profiles[0].ID,
		profiles[0].ActiveVersion.Int32).Find(&versions).Error; err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("device profile %q: active version %d is missing",
			profiles[0].Token, profiles[0].ActiveVersion.Int32)
	}
	snap, err := parseProfileSnapshot(versions[0].Snapshot)
	if err != nil {
		return nil, fmt.Errorf("device profile %q version %d cannot be read: %w",
			profiles[0].Token, profiles[0].ActiveVersion.Int32, err)
	}
	return snap.Commands, nil
}

// WindowOpening is the answer to "when may an operation under this window start?".
type WindowOpening struct {
	// Found is false when no window has the token (deleted, or never existed).
	Found bool
	// OpenNow reports whether the window is open at the instant asked about.
	OpenNow bool
	// DispatchAfter is the earliest instant an operation may start: the instant asked about
	// when the window is open, otherwise the next opening.
	DispatchAfter time.Time
	// Closes is when the interval DispatchAfter belongs to ends.
	Closes time.Time
}

// MaintenanceWindowOpening resolves a window by token to its next opening — the single entry
// point other areas use to evaluate a window (command delivery's re-stamp pass today; the
// over-the-air update gate later), so every consumer gets the calculator's answer and none
// re-implements it.
//
// A window that does not exist is reported as Found=false rather than an error: a caller
// holding a stamp from a window that was since deleted needs to tell that apart from "the
// check could not be made".
func (api *Api) MaintenanceWindowOpening(ctx context.Context, token string, at time.Time) (*WindowOpening, error) {
	matches, err := api.MaintenanceWindowsByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return &WindowOpening{Found: false}, nil
	}
	schedule, err := matches[0].Schedule()
	if err != nil {
		return nil, fmt.Errorf("maintenance window %q cannot be evaluated: %w", token, err)
	}
	iv := schedule.Next(at)
	opening := &WindowOpening{Found: true, OpenNow: !iv.Start.After(at), Closes: iv.End}
	opening.DispatchAfter = iv.Start
	if opening.OpenNow {
		opening.DispatchAfter = at
	}
	return opening, nil
}

// IsInvalidWindow reports whether err is a window validation failure (a caller fault).
func IsInvalidWindow(err error) bool { return errors.Is(err, maintwindow.ErrInvalid) }
