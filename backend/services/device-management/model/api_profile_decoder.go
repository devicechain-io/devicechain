// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/devicechain-io/dc-microservice/payloaddecode"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// This file is the authoring and publishing half of tenant-authored payload decoders: the
// profile's draft decoder document, its save-time validation, and the publish and rollback gate.
// The test door is in api_profile_decoder_preview.go. Running a decoder against live traffic is
// a later slice; nothing here changes how an event resolves.

// compileDecoder parses a stored or submitted document strictly and compiles it against the
// metrics. Every failure is the typed refusal, so a caller returns it unwrapped.
//
// 🔴 THE METRICS ARE NEVER NIL HERE. A nil payloaddecode.Metrics turns the strict-key check, the
// calculated-metric refusal and the output validation off, which is what a tool with no profile
// wants and what a save or a publish must never get: a profile with no metrics passes an empty
// list, and every output key is then undeclared.
func compileDecoder(prefix string, raw []byte, defs []*MetricDefinition) (*payloaddecode.Document, *payloaddecode.Plan, error) {
	doc, err := payloaddecode.ParseDocument(raw)
	if err != nil {
		return nil, nil, refuseDecoder(prefix, err)
	}
	plan, err := payloaddecode.Compile(doc, newDecoderMetrics(defs))
	if err != nil {
		return nil, nil, refuseDecoder(prefix, err)
	}
	return doc, plan, nil
}

// SetDeviceProfileDecoder replaces a profile's DRAFT payload decoder. The document is parsed
// strictly (an unknown key is refused, not ignored) and compiled in full against the profile's
// DRAFT metrics, so a tenant never holds an uncompilable draft: a scalar output must be a
// declared, measured, non-calculated metric, `lorawan.*` is reserved for link metrics, a key
// written twice in a section is refused, and every expression must type-check and fit its cost
// ceiling. The refusal names the failing expression by path.
//
// Saved vectors are NOT required to pass here, so an author can save work in progress; they gate
// publish (checkPayloadDecoder).
//
// It writes the draft only. Published versions are immutable, so a decoder reaches devices only
// through the next publish. Only the decoder column is written, so a racing publish or edit does
// not revert other profile fields; the converse holds because UpdateDeviceProfile omits it.
func (api *Api) SetDeviceProfileDecoder(ctx context.Context, token string, document []byte) (*DeviceProfile, error) {
	profile, err := api.deviceProfileByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	defs, err := api.MetricDefinitionsByDeviceProfile(ctx, profile.ID)
	if err != nil {
		return nil, err
	}
	if _, _, err := compileDecoder("cannot save the payload decoder", document, defs); err != nil {
		return nil, err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, document); err != nil {
		return nil, refuseDecoder("cannot save the payload decoder", err)
	}
	encoded := datatypes.JSON(compact.Bytes())
	return api.writeDecoderDeclaration(ctx, profile, &encoded)
}

// ClearDeviceProfileDecoder withdraws a profile's DRAFT decoder. Published versions keep theirs.
func (api *Api) ClearDeviceProfileDecoder(ctx context.Context, token string) (*DeviceProfile, error) {
	profile, err := api.deviceProfileByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	return api.writeDecoderDeclaration(ctx, profile, nil)
}

// writeDecoderDeclaration writes the one column. Select names it, so a nil value is stored as NULL
// rather than skipped.
func (api *Api) writeDecoderDeclaration(ctx context.Context, profile *DeviceProfile, encoded *datatypes.JSON) (*DeviceProfile, error) {
	res := api.RDB.DB(ctx).Model(profile).Where("id = ?", profile.ID).
		Select("DecoderDeclaration").
		Updates(&DeviceProfile{DecoderDeclaration: encoded})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	profile.DecoderDeclaration = encoded
	return profile, nil
}

// decoderDeclarationForProfile reads a profile's DRAFT decoder for a publish to freeze. It is not
// re-validated here: checkPayloadDecoder compiles the frozen bytes, so what is validated is what is
// frozen, with no second read in between.
func (api *Api) decoderDeclarationForProfile(ctx context.Context, profileId uint) (json.RawMessage, error) {
	profiles, err := api.DeviceProfilesById(ctx, []uint{profileId})
	if err != nil {
		return nil, err
	}
	if len(profiles) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	d := profiles[0].DecoderDeclaration
	if d == nil || len(*d) == 0 {
		return nil, nil
	}
	return json.RawMessage(*d), nil
}

// checkPayloadDecoder is the publish gate. It compiles the snapshot's decoder against the
// snapshot's own metrics, so the strict-key check sees the metrics being published, and runs every
// saved vector. It refuses the publish with the first failing expression or vector named, and
// fails closed: a snapshot it cannot parse or compile is a refusal, never a pass.
func (api *Api) checkPayloadDecoder(ctx context.Context, snapshot datatypes.JSON) error {
	return checkSnapshotDecoder(snapshot, "cannot publish device profile")
}

// checkRollbackPayloadDecoder is the same gate over a rollback target. A snapshot that compiled
// when it was published still compiles, because function-library versions only ever grow; this is
// the guard against a downgrade onto a version naming a library this build does not know.
func (api *Api) checkRollbackPayloadDecoder(ctx context.Context, snapshot datatypes.JSON, token string, version int32) error {
	return checkSnapshotDecoder(snapshot, fmt.Sprintf("cannot make version %d of device profile %q active", version, token))
}

func checkSnapshotDecoder(snapshot datatypes.JSON, prefix string) error {
	snap, err := parseProfileSnapshot(snapshot)
	if err != nil {
		return err
	}
	if len(snap.Decoder) == 0 {
		return nil
	}
	_, plan, err := compileDecoder(prefix+": the payload decoder does not compile", snap.Decoder, snap.Metrics)
	if err != nil {
		return err
	}
	for _, r := range plan.RunVectors() {
		if !r.OK {
			return &DecoderRefusalError{Code: CodeInvalidDecoder,
				Msg: fmt.Sprintf("%s: payload decoder vector %q fails: %s", prefix, r.Name, r.Error)}
		}
	}
	return nil
}
