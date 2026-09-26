// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog/log"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// resolveProfileId maps an optional profile token to a profile id for the
// DeviceType → DeviceProfile reference (ADR-045). A nil/empty token resolves to
// nil (no profile adopted — a valid, capability-limited type); an unknown token
// is a fail-closed error rather than a silently dangling reference.
func (api *Api) resolveProfileId(ctx context.Context, token *string) (*uint, error) {
	if token == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*token)
	if trimmed == "" {
		return nil, nil
	}
	matches, err := api.DeviceProfilesByToken(ctx, []string{trimmed})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("%w: device profile %q", gorm.ErrRecordNotFound, trimmed)
	}
	id := matches[0].ID
	return &id, nil
}

// profileIdForDeviceType resolves a device type's adopted profile id (ADR-045),
// the type → profile hop the capability-resolution path (device → type → profile)
// depends on. Returns (0, false) when the type is unknown or has no profile — a
// valid, capability-limited type whose devices simply have no definitions.
func (api *Api) profileIdForDeviceType(ctx context.Context, deviceTypeId uint) (uint, bool, error) {
	types, err := api.DeviceTypesById(ctx, []uint{deviceTypeId})
	if err != nil {
		return 0, false, err
	}
	if len(types) == 0 || types[0].ProfileId == nil {
		return 0, false, nil
	}
	return *types[0].ProfileId, true, nil
}

// deviceTypeIdsForProfile returns the ids of every device type that adopts the
// given profile (ADR-045). Used to fan cache eviction back out to the type keys the
// ingest path is indexed by when a profile's definitions change.
func (api *Api) deviceTypeIdsForProfile(ctx context.Context, profileId uint) ([]uint, error) {
	var ids []uint
	err := api.RDB.DB(ctx).Model(&DeviceType{}).Where("profile_id = ?", profileId).Pluck("id", &ids).Error
	return ids, err
}

// DeviceTypeCountForProfile returns how many device types currently adopt the
// given profile (ADR-045). A profile is a shared contract, so the authoring UI
// surfaces this to warn that a definition change affects every adopting type.
func (api *Api) DeviceTypeCountForProfile(ctx context.Context, profileId uint) (int32, error) {
	var count int64
	err := api.RDB.DB(ctx).Model(&DeviceType{}).Where("profile_id = ?", profileId).Count(&count).Error
	return int32(count), err
}

// Create a new device type.
func (api *Api) CreateDeviceType(ctx context.Context, request *DeviceTypeCreateRequest) (*DeviceType, error) {
	profileId, err := api.resolveProfileId(ctx, request.ProfileToken)
	if err != nil {
		return nil, err
	}
	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata)
	if err != nil {
		return nil, err
	}
	created := &DeviceType{
		TokenReference: rdb.TokenReference{
			Token: request.Token,
		},
		NamedEntity: rdb.NamedEntity{
			Name:        rdb.NullStrOf(request.Name),
			Description: rdb.NullStrOf(request.Description),
		},
		BrandedEntity: rdb.BrandedEntity{
			ImageUrl:        rdb.NullStrOf(request.ImageUrl),
			Icon:            rdb.NullStrOf(request.Icon),
			BackgroundColor: rdb.NullStrOf(request.BackgroundColor),
			ForegroundColor: rdb.NullStrOf(request.ForegroundColor),
			BorderColor:     rdb.NullStrOf(request.BorderColor),
		},
		MetadataEntity: rdb.MetadataEntity{
			Metadata: metadataJSON,
		},
		ProfileId:    profileId,
		Manufacturer: rdb.NullStrOf(request.Manufacturer),
		ModelName:    rdb.NullStrOf(request.Model),
	}
	result := api.RDB.DB(ctx).Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Update an existing device type, applying only the fields the caller actually
// sent. The entity is looked up by the `token` ARGUMENT — the request payload no
// longer carries one, so an update cannot move a type's token.
//
// Each assignment folds the field's three states onto the stored value: absent
// keeps it, null clears it, a value sets it. Reading `found.X` as the "current"
// argument is what makes an omitted field a no-op, so these must stay assignments
// FROM the loaded record rather than from the request alone.
func (api *Api) UpdateDeviceType(ctx context.Context, token string,
	request *DeviceTypeUpdateRequest) (*DeviceType, error) {
	matches, err := api.DeviceTypesByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	found := matches[0]
	oldProfileId := found.ProfileId

	// The profile hop resolves BEFORE anything is written, so an unknown profile
	// token rejects the whole update rather than half-applying it. An absent
	// profileToken must not re-resolve at all — resolving the type's existing
	// profile token here would turn a rename into a profile re-point whenever the
	// referenced profile had since been deleted.
	profileId := found.ProfileId
	if request.ProfileToken.Set {
		profileId, err = api.resolveProfileId(ctx, request.ProfileToken.Value)
		if err != nil {
			return nil, err
		}
	}

	found.Name = request.Name.ApplyToNullString(found.Name)
	found.Description = request.Description.ApplyToNullString(found.Description)
	found.ImageUrl = request.ImageUrl.ApplyToNullString(found.ImageUrl)
	found.Icon = request.Icon.ApplyToNullString(found.Icon)
	found.BackgroundColor = request.BackgroundColor.ApplyToNullString(found.BackgroundColor)
	found.ForegroundColor = request.ForegroundColor.ApplyToNullString(found.ForegroundColor)
	found.BorderColor = request.BorderColor.ApplyToNullString(found.BorderColor)
	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata.ApplyTo(dcgraphql.MetadataStr(found.Metadata)))
	if err != nil {
		return nil, err
	}
	found.Metadata = metadataJSON
	found.ProfileId = profileId
	found.Manufacturer = request.Manufacturer.ApplyToNullString(found.Manufacturer)
	found.ModelName = request.Model.ApplyToNullString(found.ModelName)

	repointed := !uintPtrEqual(oldProfileId, profileId)
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(found).Error; err != nil {
			return err
		}
		if !repointed {
			return nil
		}
		// A re-point moves every device of the type onto another profile, so each one's
		// membership begins NOW — stored in the same transaction as the re-point, because the
		// roster facts below and the detection engine's reconcile both read it back
		// (rosterEntries). UpdateColumn, not Update: a membership change is not an edit of the
		// device, so its updated_at stays.
		return tx.Model(&Device{}).Where("device_type_id = ?", found.ID).
			UpdateColumn("expected_since", time.Now().UTC().Truncate(time.Microsecond)).Error
	})
	if err != nil {
		return nil, err
	}
	// Re-roster POST-COMMIT when the type's adopted profile changed (ADR-051 slice 4c-2):
	// re-pointing a type's profile silently re-binds EVERY device of that type, so the
	// per-device roster rows event-processing armed absence from must follow — otherwise a
	// tenant that adopts a profile onto an existing type (the feature's own migration path)
	// gets zero rostered devices, and a re-point leaves dead-men armed under the old profile.
	// A fan-out over the type's devices is right here: this is a rare admin mutation and the
	// fact is tiny and best-effort — a device missed here is repaired by event-processing's
	// reconcile against this service (at its leadership-term start and every five minutes).
	if repointed {
		api.emitDeviceRosterForType(ctx, found.ID)
	}
	return found, nil
}

// uintPtrEqual reports whether two optional foreign keys name the same profile (both
// nil, or both non-nil and equal) — the "did the adopted profile change" test.
func uintPtrEqual(a, b *uint) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// Get device types by id.
func (api *Api) DeviceTypesById(ctx context.Context, ids []uint) ([]*DeviceType, error) {
	return rdb.FindByIds[DeviceType](api.RDB.DB(ctx), ids)
}

// Get device types by token.
func (api *Api) DeviceTypesByToken(ctx context.Context, tokens []string) ([]*DeviceType, error) {
	found := make([]*DeviceType, 0)
	result := api.RDB.DB(ctx).Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for device types that meet criteria.
func (api *Api) DeviceTypes(ctx context.Context, criteria DeviceTypeSearchCriteria) (*DeviceTypeSearchResults, error) {
	results := make([]DeviceType, 0)
	db, pag := api.RDB.ListOf(ctx, &DeviceType{}, nil, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &DeviceTypeSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// Create a new device.
func (api *Api) CreateDevice(ctx context.Context, request *DeviceCreateRequest) (*Device, error) {
	matches, err := api.DeviceTypesByToken(ctx, []string{request.DeviceTypeToken})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata)
	if err != nil {
		return nil, err
	}
	created := &Device{
		TokenReference: rdb.TokenReference{
			Token: request.Token,
		},
		ExternalReference: rdb.ExternalReference{
			ExternalId: rdb.NullStrOf(request.ExternalId),
		},
		NamedEntity: rdb.NamedEntity{
			Name:        rdb.NullStrOf(request.Name),
			Description: rdb.NullStrOf(request.Description),
		},
		MetadataEntity: rdb.MetadataEntity{
			Metadata: metadataJSON,
		},
		DeviceType: matches[0],
	}
	result := api.RDB.DB(ctx).Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	// Roster the new device with event-processing (ADR-051 slice 4c-2), POST-COMMIT and
	// best-effort, so the DETECT engine can arm absence for it even if it never reports.
	// The entry is read back (rosterEntries): the profile its just-bound type adopts, and
	// expected-since = the row's creation time (the dead-man clock base). A resolution/emit
	// failure never fails the create — the device is durable; a missed roster fact is repaired
	// by event-processing's reconcile against this service (at its leadership-term start and
	// every five minutes).
	api.emitDeviceRosterForDevice(ctx, created)
	return created, nil
}

// CreateDevices creates many devices in one transaction, so a bulk provisioning
// (a whole fleet) either fully applies or not at all — the same all-or-nothing
// contract CreateEntityRelationships gives bulk assignment. Each request's device
// type is resolved from its token like the single create, but the DISTINCT types
// are resolved once up front rather than once per device, so provisioning N
// devices of one type does one type lookup, not N.
//
// The device-roster emits (ADR-051 slice 4c-2) happen AFTER the transaction
// commits, one per created device, matching the single create's post-commit
// best-effort contract: a roster failure never rolls back a durable device, and a
// missed roster fact is repaired by event-processing's reconcile against this
// service. Emitting inside the transaction would publish a roster fact for a device
// a later request in the same batch could still roll back.
func (api *Api) CreateDevices(ctx context.Context, requests []*DeviceCreateRequest) ([]*Device, error) {
	if len(requests) == 0 {
		return []*Device{}, nil
	}

	// Resolve each distinct device-type token once. An unknown type fails the
	// whole batch (fail-closed) rather than silently dropping the devices that
	// referenced it — the transaction below makes that all-or-nothing.
	typesByToken := make(map[string]*DeviceType)
	distinct := make([]string, 0)
	for _, request := range requests {
		if _, ok := typesByToken[request.DeviceTypeToken]; !ok {
			typesByToken[request.DeviceTypeToken] = nil
			distinct = append(distinct, request.DeviceTypeToken)
		}
	}
	matches, err := api.DeviceTypesByToken(ctx, distinct)
	if err != nil {
		return nil, err
	}
	for _, m := range matches {
		typesByToken[m.Token] = m
	}

	// Convert every row's metadata BEFORE the transaction opens, for the same reason the
	// device types are resolved up here: a bad row is a property of the request, and a
	// request that cannot succeed should not first insert N-1 rows and roll them back. It
	// also means the refusal names the offending device without the caller having to
	// correlate it against a transaction that left nothing behind.
	metadataByIndex := make([]*datatypes.JSON, len(requests))
	for i, request := range requests {
		metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata)
		if err != nil {
			return nil, fmt.Errorf("device %q: %w", request.Token, err)
		}
		metadataByIndex[i] = metadataJSON
	}

	created := make([]*Device, 0, len(requests))
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		for i, request := range requests {
			dt := typesByToken[request.DeviceTypeToken]
			if dt == nil {
				return fmt.Errorf("device type %q: %w", request.DeviceTypeToken, gorm.ErrRecordNotFound)
			}
			metadataJSON := metadataByIndex[i]
			device := &Device{
				TokenReference:    rdb.TokenReference{Token: request.Token},
				ExternalReference: rdb.ExternalReference{ExternalId: rdb.NullStrOf(request.ExternalId)},
				NamedEntity: rdb.NamedEntity{
					Name:        rdb.NullStrOf(request.Name),
					Description: rdb.NullStrOf(request.Description),
				},
				MetadataEntity: rdb.MetadataEntity{Metadata: metadataJSON},
				// Set the FK by id (not the association) so a bulk create of N
				// devices sharing a type does not re-touch device_types per row.
				DeviceTypeId: dt.ID,
			}
			if err := tx.Create(device).Error; err != nil {
				return err
			}
			device.DeviceType = dt
			created = append(created, device)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Post-commit, best-effort roster emit (see the doc comment). The entries are read
	// back in chunks, so a 1000-device batch is a couple of joined reads after the
	// transaction rather than a lookup per device.
	api.emitDeviceRosterBatch(ctx, created)
	return created, nil
}

// rosterEmitChunk bounds the id list of one read-back in emitDeviceRosterBatch, so a bulk
// create of MaxBulkDeviceCount devices is a handful of statements with bounded parameter lists.
const rosterEmitChunk = 500

// emitDeviceRosterBatch emits a device-roster fact (ADR-051 slice 4c-2) for every
// device in a bulk create, POST-COMMIT and best-effort — semantically identical to
// emitDeviceRosterForDevice per device, but reading the entries back a chunk at a time
// rather than one device at a time. A nil publisher short-circuits before any read. A read
// error skips that chunk's devices; each is repaired by event-processing's reconcile
// against this service, exactly like the single-device path.
func (api *Api) emitDeviceRosterBatch(ctx context.Context, devices []*Device) {
	if api.DeviceRosterPublisher == nil {
		return // no publisher wired: skip the read-back entirely
	}
	for start := 0; start < len(devices); start += rosterEmitChunk {
		end := min(start+rosterEmitChunk, len(devices))
		ids := make([]uint, 0, end-start)
		for _, d := range devices[start:end] {
			ids = append(ids, d.ID)
		}
		api.emitRosterEntries(ctx, func(q *gorm.DB) *gorm.DB { return q.Where("devices.id IN ?", ids) },
			"devices", fmt.Sprintf("%d created devices", len(ids)))
	}
}

// emitRosterEntries reads roster entries back through rosterEntries — the query the detection
// engine's reconcile reads, so the fact and the repair cannot disagree — and emits one fact per
// device. A read error is logged and swallowed (what names the skipped devices for the log).
func (api *Api) emitRosterEntries(ctx context.Context, where func(*gorm.DB) *gorm.DB, key, what string) {
	entries, err := api.rosterEntries(ctx, where)
	if err != nil {
		log.Error().Err(err).Str(key, what).
			Msg("Unable to read device roster entries; skipping roster emit (the detection engine's reconcile repairs it).")
		return
	}
	for _, e := range entries {
		api.emitDeviceRoster(ctx, &DeviceRosterEvent{
			DeviceToken:   e.DeviceToken,
			ProfileToken:  e.ProfileToken,
			ExpectedSince: e.ExpectedSince,
		})
	}
}

// CreateDevicesFromTemplate renders a bulk request's templates into concrete
// device create requests and creates them transactionally (CreateDevices). The
// render + validate step is pure (expandBulkDeviceRequest) and fails the whole
// call before any write if the request is malformed — count out of range, a
// token template that would not vary per device, or a rendered token that breaks
// the grammar or collides within the batch.
func (api *Api) CreateDevicesFromTemplate(ctx context.Context, request *DeviceBulkCreateRequest) ([]*Device, error) {
	requests, err := expandBulkDeviceRequest(request)
	if err != nil {
		return nil, err
	}
	return api.CreateDevices(ctx, requests)
}

// expandBulkDeviceRequest renders a DeviceBulkCreateRequest into per-device create
// requests. Pure (no DB, no clock) so it is exhaustively unit-testable: the only
// randomness is the external-id "{random}" substitution, and a caller that needs
// determinism simply omits it. It rejects, fail-closed:
//   - a count below 1 or above MaxBulkDeviceCount;
//   - an empty tokenTemplate, or one with no index placeholder (every device would
//     render the same token and collide on the unique key — caught here with a
//     clear message rather than as an opaque duplicate-key error mid-transaction);
//   - a rendered token that violates the global token grammar;
//   - a rendered token that repeats within the batch (belt-and-braces alongside the
//     index-placeholder check — e.g. a pathological template).
func expandBulkDeviceRequest(request *DeviceBulkCreateRequest) ([]*DeviceCreateRequest, error) {
	if request == nil {
		return nil, fmt.Errorf("bulk create request is required")
	}
	count := int(request.Count)
	if count < 1 {
		return nil, fmt.Errorf("count must be at least 1")
	}
	if count > MaxBulkDeviceCount {
		return nil, fmt.Errorf("count %d exceeds the maximum of %d devices per bulk create", count, MaxBulkDeviceCount)
	}
	if strings.TrimSpace(request.TokenTemplate) == "" {
		return nil, fmt.Errorf("tokenTemplate is required")
	}
	// Bound every template's length and pad width BEFORE rendering anything: an
	// unbounded "{n:0Wd}" pad renders a megabyte per placeholder, which across a
	// 1000-device batch is a memory-amplification DoS on this shared service. "{random}"
	// is only meaningful for external ids (a fresh business id per device); in a token
	// or name template it is left literal, so reject it there rather than silently
	// storing "{random}" or failing the token grammar with a confusing message.
	if err := core.ValidateTemplate(request.TokenTemplate); err != nil {
		return nil, fmt.Errorf("tokenTemplate: %w", err)
	}
	if strings.Contains(request.TokenTemplate, "{random}") {
		return nil, fmt.Errorf("tokenTemplate must not use {random}; a token must be reproducible — use {n} or {n:0Wd}")
	}
	if !core.HasIndexPlaceholder(request.TokenTemplate) {
		return nil, fmt.Errorf("tokenTemplate %q must contain an index placeholder ({n} or {n:0Wd}) so each device gets a distinct token", request.TokenTemplate)
	}
	if request.NameTemplate != nil {
		if err := core.ValidateTemplate(*request.NameTemplate); err != nil {
			return nil, fmt.Errorf("nameTemplate: %w", err)
		}
		if strings.Contains(*request.NameTemplate, "{random}") {
			return nil, fmt.Errorf("nameTemplate must not use {random}; it is only meaningful for external ids")
		}
	}
	if request.ExternalIdTemplate != nil {
		if err := core.ValidateTemplate(*request.ExternalIdTemplate); err != nil {
			return nil, fmt.Errorf("externalIdTemplate: %w", err)
		}
	}
	// A provided external-id template must VARY per device when creating more than
	// one: devices carry a per-tenant unique index on external_id (partial, non-null),
	// so a fixed external-id would render N identical ids and the whole batch would
	// fail on the unique constraint deep in the transaction. Catch it here with a
	// clear message. "{random}" counts as varying (a fresh id per device); an index
	// placeholder counts; a literal string does not. A single device (count == 1) may
	// carry any fixed external id.
	if request.ExternalIdTemplate != nil {
		ext := strings.TrimSpace(*request.ExternalIdTemplate)
		varying := core.HasIndexPlaceholder(ext) || strings.Contains(ext, "{random}")
		if ext != "" && count > 1 && !varying {
			return nil, fmt.Errorf("externalIdTemplate %q must vary per device (include {n}, {n:0Wd} or {random}) when creating more than one device, since external ids are unique per tenant", ext)
		}
	}

	// Indices are 1-based. A start below 1 is rejected rather than rendered: a
	// negative index pads differently than a client preview expects ("%0*d" places
	// the sign before the zero padding) and a zero/negative index is not a
	// meaningful device number.
	start := 1
	if request.StartIndex != nil {
		start = int(*request.StartIndex)
	}
	if start < 1 {
		return nil, fmt.Errorf("startIndex must be at least 1")
	}

	out := make([]*DeviceCreateRequest, 0, count)
	seen := make(map[string]bool, count)
	for k := 0; k < count; k++ {
		i := start + k
		token := core.RenderTemplate(request.TokenTemplate, i, nil)
		if err := core.ValidateToken(token); err != nil {
			return nil, fmt.Errorf("rendered token at index %d: %w", i, err)
		}
		if seen[token] {
			return nil, fmt.Errorf("rendered token %q is not unique across the batch", token)
		}
		seen[token] = true

		dr := &DeviceCreateRequest{
			Token:           token,
			DeviceTypeToken: request.DeviceTypeToken,
			Metadata:        request.Metadata,
		}
		if request.NameTemplate != nil {
			name := core.RenderTemplate(*request.NameTemplate, i, nil)
			// Rendered values must fit their storage columns (name size:128,
			// external_id size:256) — checked here so an over-long render is a clear
			// per-index error, not a truncation or an opaque DB failure mid-batch.
			if len(name) > maxDeviceNameLen {
				return nil, fmt.Errorf("rendered name at index %d is %d characters, exceeding the maximum of %d", i, len(name), maxDeviceNameLen)
			}
			dr.Name = &name
		}
		if request.ExternalIdTemplate != nil {
			ext := core.RenderTemplate(*request.ExternalIdTemplate, i, core.RandomHexToken)
			if len(ext) > maxDeviceExternalIdLen {
				return nil, fmt.Errorf("rendered external id at index %d is %d characters, exceeding the maximum of %d", i, len(ext), maxDeviceExternalIdLen)
			}
			dr.ExternalId = &ext
		}
		out = append(out, dr)
	}
	return out, nil
}

// Storage-column bounds for rendered device fields (rdb.NamedEntity.Name is
// size:128, rdb.ExternalReference.ExternalId is size:256). Mirrored here so a
// bulk render that would overflow a column fails with a clear per-index error
// instead of a truncation or an opaque driver error inside the transaction.
const (
	maxDeviceNameLen       = 128
	maxDeviceExternalIdLen = 256
)

// emitDeviceRosterForDevice emits a device-roster fact for one device (ADR-051 slice 4c-2),
// read back through rosterEntries: the stable token of the profile its type adopts, and the
// base of the never-reported dead-man clock — the device's creation time, or the moment its
// current membership began when a re-type stored one (an old device re-typed into a profile
// whose absence rule was published long ago must get a fresh timeout of grace, not an instant
// fire: the fleet-burst the grace-period base exists to prevent). Best-effort: a read error is
// logged and swallowed rather than failing the caller's already-committed write — the roster is
// a convenience projection, never the source of truth.
func (api *Api) emitDeviceRosterForDevice(ctx context.Context, device *Device) {
	if api.DeviceRosterPublisher == nil {
		return // no publisher wired: skip the read-back entirely
	}
	api.emitRosterEntries(ctx, func(q *gorm.DB) *gorm.DB { return q.Where("devices.id = ?", device.ID) },
		"device", device.Token)
}

// emitDeviceRosterForType fans a device-roster fact out to every device of a type after
// the type's adopted profile changed (ADR-051 slice 4c-2), so the roster's device→profile
// binding follows a type re-point. UpdateDeviceType stored expected_since = the re-point on
// every device of the type in the same transaction: the membership under the new profile
// begins there, so each device gets a fresh timeout of grace before absence can fire. The
// entries are read back through rosterEntries. Best-effort like the per-device path — a read
// error is logged and skipped, and event-processing's reconcile against this service repairs
// any device missed here.
func (api *Api) emitDeviceRosterForType(ctx context.Context, deviceTypeId uint) {
	if api.DeviceRosterPublisher == nil {
		return
	}
	api.emitRosterEntries(ctx, func(q *gorm.DB) *gorm.DB { return q.Where("devices.device_type_id = ?", deviceTypeId) },
		"deviceType", strconv.FormatUint(uint64(deviceTypeId), 10))
}

// Update an existing device, applying only the fields the caller actually sent.
//
// The device is located by the `token` ARGUMENT. It used to be located by
// request.Token with the argument ignored outright, so a caller who named one
// device in the argument and another in the payload silently updated the second and
// got a 200 for it. The payload no longer carries a token at all.
func (api *Api) UpdateDevice(ctx context.Context, token string, request *DeviceUpdateRequest) (*Device, error) {
	matches, err := api.DevicesByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	updated := matches[0]

	// The type hop resolves BEFORE anything is written, so an unknown device type
	// refuses the WHOLE update rather than applying the fields it liked first. The
	// nil guard is not decoration: the preload comes back nil for a dangling FK, and
	// the comparison this replaces dereferenced it unconditionally.
	currentTypeToken := ""
	if updated.DeviceType != nil {
		currentTypeToken = updated.DeviceType.Token
	}
	retypeTo, retyped, err := resolveRequiredTypeRef(request.DeviceTypeToken, currentTypeToken, "deviceTypeToken")
	if err != nil {
		return nil, err
	}
	if retyped {
		types, err := api.DeviceTypesByToken(ctx, []string{retypeTo})
		if err != nil {
			return nil, err
		}
		if len(types) == 0 {
			return nil, gorm.ErrRecordNotFound
		}
		updated.DeviceType = types[0]
		// The association is what gorm writes; the FK is set explicitly because the
		// post-commit roster resolve below reads DeviceTypeId directly, before any
		// reload would have refreshed it from the association.
		updated.DeviceTypeId = types[0].ID
	}

	updated.ExternalId = request.ExternalId.ApplyToNullString(updated.ExternalId)
	updated.Name = request.Name.ApplyToNullString(updated.Name)
	updated.Description = request.Description.ApplyToNullString(updated.Description)
	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata.ApplyTo(dcgraphql.MetadataStr(updated.Metadata)))
	if err != nil {
		return nil, err
	}
	updated.Metadata = metadataJSON
	if retyped {
		// The membership of the (possibly new) profile begins now, and it is STORED with the
		// re-type: the roster fact below and the detection engine's reconcile both read it back.
		updated.ExpectedSince = sql.NullTime{Time: time.Now().UTC().Truncate(time.Microsecond), Valid: true}
	}

	result := api.RDB.DB(ctx).Save(updated)
	if result.Error != nil {
		return nil, result.Error
	}
	// Re-roster POST-COMMIT only when the device was re-typed (ADR-051 slice 4c-2): a
	// re-type may change the adopted profile, so the roster's device→profile binding must
	// follow. Metadata-only updates leave the binding unchanged, so they emit nothing. The
	// dead-man clock base is the membership-began instant stored above, not the device's
	// creation time — a device moved to a new profile gets a fresh grace window, and re-using
	// the old creation time would instantly fire absence under a long-standing rule. (A re-type
	// between two types that adopt the SAME profile also emits and refreshes the window; that is
	// a benign fresh grace, never a false fire, and not worth a second profile resolve to
	// suppress.) Best-effort, exactly like the create path.
	//
	// `retyped` now means "the caller named a DIFFERENT type", where it used to mean
	// "the caller named a type that differs from the stored one" — a full replace had
	// to send the token on every update, so an unchanged token was the common case and
	// the comparison did the filtering. Under partial semantics an untouched type is
	// simply absent, and resolveRequiredTypeRef still filters a re-send of the same
	// token, so the emission set is unchanged.
	//
	// A device token rename remains unreachable through this method — the update input
	// carries no token — so the roster's device-token key is stable here; a future
	// rename path would need its own re-roster/removal.
	if retyped {
		api.emitDeviceRosterForDevice(ctx, updated)
	}
	return updated, nil
}

// Get devices by id.
func (api *Api) DevicesById(ctx context.Context, ids []uint) ([]*Device, error) {
	return rdb.FindByIds[Device](api.RDB.DB(ctx).Preload("DeviceType"), ids)
}

// Get devices by token.
func (api *Api) DevicesByToken(ctx context.Context, tokens []string) ([]*Device, error) {
	found := make([]*Device, 0)
	result := api.RDB.DB(ctx)
	result = result.Preload("DeviceType")
	result = result.Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get devices by their customer-owned external id (ADR-049). Only rows that carry
// a matching non-null external id are returned; the token remains the addressing id.
func (api *Api) DevicesByExternalId(ctx context.Context, externalIds []string) ([]*Device, error) {
	found := make([]*Device, 0)
	result := api.RDB.DB(ctx)
	result = result.Preload("DeviceType")
	result = result.Find(&found, "external_id in ?", externalIds)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for devices that meet criteria.
func (api *Api) Devices(ctx context.Context, criteria DeviceSearchCriteria) (*DeviceSearchResults, error) {
	results := make([]Device, 0)
	db, pag := api.RDB.ListOf(ctx, &Device{}, func(result *gorm.DB) *gorm.DB {
		if criteria.DeviceType != nil {
			result = result.Where("device_type_id = (?)",
				api.RDB.DB(ctx).Model(&DeviceType{}).Select("id").Where("token = ?", criteria.DeviceType))
		}
		return result.Preload("DeviceType")
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &DeviceSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}
