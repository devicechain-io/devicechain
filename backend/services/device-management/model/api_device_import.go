// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/devicechain-io/dc-microservice/conflict"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/sqlnull"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// MaxDeviceImportRows bounds the rows one importDevices call carries. Each row is its own
// transaction, so the bound is on how long one request holds a connection pool slot, and it
// matches the template bulk create's bound. A larger file is sent as several calls.
const MaxDeviceImportRows = MaxBulkDeviceCount

// Import modes: how an import treats credentials. There is deliberately no mode that
// generates a secret — the platform never authors a secret-bearing file.
const (
	// DeviceImportModeNone creates devices only. Credential columns are refused.
	DeviceImportModeNone = "NONE"
	// DeviceImportModeCredentials creates each device and the credential a factory supplied
	// for it in one transaction.
	DeviceImportModeCredentials = "FACTORY_CREDENTIALS"
)

// Row outcomes.
const (
	DeviceImportCreated  = "CREATED"
	DeviceImportExists   = "EXISTS"
	DeviceImportConflict = "CONFLICT"
	DeviceImportInvalid  = "INVALID"
	DeviceImportRefused  = "REFUSED"
)

// Codes a row outcome carries besides conflict.Code and limit.Code.
const (
	importCodeInvalidValue    = "INVALID_VALUE"
	importCodeNotFound        = "NOT_FOUND"
	importCodeDuplicateInFile = "DUPLICATE_IN_FILE"
	importCodeUnsupported     = "UNSUPPORTED"
)

// DeviceImportRow is one device of an import file. It carries the fields of a single
// create, plus the credential a factory supplied. CredentialValue is write-only: it is
// digested exactly like createDeviceCredential's and appears in no outcome, message or log.
type DeviceImportRow struct {
	Token           string
	DeviceTypeToken string
	ExternalId      *string
	Name            *string
	Description     *string
	Metadata        *string
	CredentialType  *string
	CredentialId    *string
	CredentialValue *string
}

// DeviceImportRowResult is the outcome of one row. Row is 1-based within the call. Code and
// Message are set for every outcome but a plain CREATED/EXISTS, and never carry a secret.
type DeviceImportRowResult struct {
	Row     int32
	Token   string
	Outcome string
	Code    *string
	Message *string
}

// DeviceImportResult is the per-row outcomes of one importDevices call, with their tallies.
type DeviceImportResult struct {
	DryRun   bool
	Rows     []*DeviceImportRowResult
	Created  int32
	Exists   int32
	Conflict int32
	Invalid  int32
	Refused  int32
}

func (r *DeviceImportResult) set(i int, row *DeviceImportRow, outcome, code, message string) {
	res := &DeviceImportRowResult{Row: int32(i + 1), Outcome: outcome}
	if row != nil {
		res.Token = row.Token
	}
	if code != "" {
		res.Code = &code
	}
	if message != "" {
		res.Message = &message
	}
	r.Rows[i] = res
}

func (r *DeviceImportResult) tally() {
	for _, res := range r.Rows {
		switch res.Outcome {
		case DeviceImportCreated:
			r.Created++
		case DeviceImportExists:
			r.Exists++
		case DeviceImportConflict:
			r.Conflict++
		case DeviceImportInvalid:
			r.Invalid++
		case DeviceImportRefused:
			r.Refused++
		}
	}
}

// importRow is a row that passed the pure checks and still has to be decided against the
// database.
type importRow struct {
	idx      int
	row      *DeviceImportRow
	name     string // trimmed, "" for none
	desc     string
	external string
	metadata *string // canonical JSON text for the stored column, nil for none
	credType string
	credID   string
	// credStored is credID as the credential_id column holds it (see storedCredentialId):
	// the keyed digest for an ACCESS_TOKEN. Set by decideImportRows, which has the key.
	credStored string
}

func importDescription(row *DeviceImportRow) (name, desc, external string) {
	return sqlnull.Text(row.Name).String, sqlnull.Text(row.Description).String, sqlnull.Text(row.ExternalId).String
}

func blank(s *string) bool { return s == nil || strings.TrimSpace(*s) == "" }

// validateImportRow is the pure half of the checks: everything decidable from the row alone,
// by the same rules a single create applies. It returns the outcome, code and message of the
// refusal, or an empty outcome when the row may proceed.
func validateImportRow(row *DeviceImportRow, mode string) (outcome, code, message string, parsed *importRow) {
	if row == nil {
		return DeviceImportInvalid, importCodeInvalidValue, "the row is empty", nil
	}
	if err := core.ValidateToken(row.Token); err != nil {
		return DeviceImportInvalid, importCodeInvalidValue,
			fmt.Sprintf("token must be 1 to %d letters, digits, hyphens or underscores, starting with a letter or digit", core.MaxTokenLen), nil
	}
	if strings.TrimSpace(row.DeviceTypeToken) == "" {
		return DeviceImportInvalid, importCodeInvalidValue, "deviceTypeToken is required", nil
	}
	// A NUL byte is not text any column can hold; the database would refuse the whole row
	// with a message about its internals, so it is refused here, naming the field.
	for field, v := range map[string]*string{
		"deviceTypeToken": &row.DeviceTypeToken, "externalId": row.ExternalId, "name": row.Name,
		"description": row.Description, "metadata": row.Metadata, "credentialType": row.CredentialType,
		"credentialId": row.CredentialId, "credentialValue": row.CredentialValue,
	} {
		if v != nil && strings.ContainsRune(*v, 0) {
			return DeviceImportInvalid, importCodeInvalidValue, field + " must not contain a NUL character", nil
		}
	}
	name, desc, external := importDescription(row)
	if utf8.RuneCountInString(name) > maxDeviceNameLen {
		return DeviceImportInvalid, limit.Code, fmt.Sprintf("name exceeds the maximum of %d characters", maxDeviceNameLen), nil
	}
	if utf8.RuneCountInString(external) > maxDeviceExternalIdLen {
		return DeviceImportInvalid, limit.Code, fmt.Sprintf("externalId exceeds the maximum of %d characters", maxDeviceExternalIdLen), nil
	}
	if utf8.RuneCountInString(desc) > maxDeviceDescriptionLen {
		return DeviceImportInvalid, limit.Code, fmt.Sprintf("description exceeds the maximum of %d characters", maxDeviceDescriptionLen), nil
	}
	var metadata *string
	if md, err := rdb.JSONInputOf("metadata", row.Metadata); err != nil {
		if _, ok := err.(*limit.Error); ok {
			return DeviceImportInvalid, limit.Code, err.Error(), nil
		}
		return DeviceImportInvalid, importCodeInvalidValue, err.Error(), nil
	} else if md != nil {
		s := string(*md)
		metadata = &s
	}

	p := &importRow{row: row, name: name, desc: desc, external: external, metadata: metadata}

	hasCredential := !blank(row.CredentialType) || !blank(row.CredentialId) || (row.CredentialValue != nil && *row.CredentialValue != "")
	if mode != DeviceImportModeCredentials {
		if hasCredential {
			return DeviceImportInvalid, importCodeInvalidValue,
				fmt.Sprintf("credential columns are not accepted in mode %s", mode), nil
		}
		return "", "", "", p
	}

	if blank(row.CredentialType) || blank(row.CredentialId) {
		return DeviceImportInvalid, importCodeInvalidValue, "credentialType and credentialId are required in mode " + mode, nil
	}
	ctype := strings.TrimSpace(*row.CredentialType)
	switch {
	case CredentialType(ctype).Retired():
		return DeviceImportRefused, importCodeUnsupported, (&UnsupportedCredentialTypeError{Type: CredentialType(ctype)}).Error(), nil
	case !CredentialType(ctype).Valid():
		return DeviceImportInvalid, importCodeInvalidValue, "credentialType must be ACCESS_TOKEN or MQTT_BASIC", nil
	}
	credID := *row.CredentialId
	if utf8.RuneCountInString(credID) > maxCredentialIdLen {
		return DeviceImportInvalid, limit.Code, fmt.Sprintf("credentialId exceeds the maximum of %d characters", maxCredentialIdLen), nil
	}
	// An access token is held as a digest of its bytes, and the limit on those is bytes.
	if CredentialType(ctype) == CredentialAccessToken && len(credID) > credential.MaxAccessTokenBytes {
		return DeviceImportInvalid, limit.Code, fmt.Sprintf("an access token credentialId exceeds the maximum of %d bytes", credential.MaxAccessTokenBytes), nil
	}
	if len(row.Token)+len(importCredentialTokenSuffix) > core.MaxTokenLen {
		return DeviceImportInvalid, limit.Code,
			fmt.Sprintf("token is too long to derive a credential token (maximum %d characters with a credential)", core.MaxTokenLen-len(importCredentialTokenSuffix)), nil
	}
	secret := ""
	if row.CredentialValue != nil {
		secret = *row.CredentialValue
	}
	if credentialRequiresSecret(ctype) {
		if len(secret) > credential.MaxDeviceSecretBytes {
			return DeviceImportInvalid, limit.Code,
				fmt.Sprintf("credentialValue exceeds the maximum of %d bytes", credential.MaxDeviceSecretBytes), nil
		}
		if len(secret) < credential.MinDeviceSecretBytes {
			return DeviceImportInvalid, importCodeInvalidValue, ErrCredentialSecretTooShort.Error(), nil
		}
	} else if secret != "" {
		// A single create drops a value sent with a type that compares no secret. An import
		// refuses it instead: a file column that looks stored and is not is worse than an error.
		return DeviceImportInvalid, importCodeInvalidValue, "credentialValue is only used by an MQTT_BASIC credential", nil
	}
	p.credType, p.credID = ctype, credID
	return "", "", "", p
}

const (
	maxDeviceDescriptionLen     = 1024
	maxCredentialIdLen          = 256
	importCredentialTokenSuffix = "-credential"
)

// metadataEqual compares two stored JSON documents by value, so key order and whitespace do
// not make an identical row differ. Absent and empty are the same.
func metadataEqual(stored []byte, incoming *string) bool {
	storedEmpty := len(bytes.TrimSpace(stored)) == 0
	incomingEmpty := incoming == nil
	if storedEmpty || incomingEmpty {
		return storedEmpty && incomingEmpty
	}
	var a, b any
	if json.Unmarshal(stored, &a) != nil || json.Unmarshal([]byte(*incoming), &b) != nil {
		return false
	}
	return reflect.DeepEqual(a, b)
}

// differingFields names the fields of the stored device that the row would change. Names
// only: a value is never echoed.
func differingFields(d *Device, p *importRow) []string {
	var diff []string
	storedType := ""
	if d.DeviceType != nil {
		storedType = d.DeviceType.Token
	}
	if storedType != strings.TrimSpace(p.row.DeviceTypeToken) {
		diff = append(diff, "deviceTypeToken")
	}
	if d.ExternalId.String != p.external || d.ExternalId.Valid != (p.external != "") {
		diff = append(diff, "externalId")
	}
	if d.Name.String != p.name || d.Name.Valid != (p.name != "") {
		diff = append(diff, "name")
	}
	if d.Description.String != p.desc || d.Description.Valid != (p.desc != "") {
		diff = append(diff, "description")
	}
	var stored []byte
	if d.Metadata != nil {
		stored = []byte(*d.Metadata)
	}
	if !metadataEqual(stored, p.metadata) {
		diff = append(diff, "metadata")
	}
	return diff
}

// ImportDevices creates the devices of an import file, one transaction per row, and reports
// what happened to each row. It is CREATE-ONLY and idempotent on the device token: a row
// whose device already exists identically is EXISTS (a re-run of the same file is all
// EXISTS), a row whose device exists differently is CONFLICT, and nothing is ever updated.
//
// Every row goes through the rules a single create applies: the token grammar, field
// bounds, valid JSON metadata, an existing device type, a free token and external id. The
// bounds a single create enforces are the only ones there are — creating a device has no
// per-tenant quota — so there is nothing further to apply per row; the batch itself is
// bounded by MaxDeviceImportRows. A dry run decides every row by the same code and writes
// nothing.
//
// One bad row never affects another. A failure that is not a property of a row (the
// database going away) stops the call with an error; rows already created report EXISTS on
// a re-run.
//
// 🔴 A credential secret is digested by the same function createDeviceCredential uses and is
// never logged, echoed or placed in an outcome: messages are built from field names only.
func (api *Api) ImportDevices(ctx context.Context, rows []*DeviceImportRow, mode string, dryRun bool) (*DeviceImportResult, error) {
	switch mode {
	case DeviceImportModeNone, DeviceImportModeCredentials:
	default:
		return nil, fmt.Errorf("mode must be %s or %s", DeviceImportModeNone, DeviceImportModeCredentials)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("rows must not be empty")
	}
	if len(rows) > MaxDeviceImportRows {
		return nil, limit.Exceeded("rows in one import", len(rows), MaxDeviceImportRows)
	}
	if mode == DeviceImportModeCredentials && api.DeviceSecretKey == nil {
		return nil, errNoDeviceSecretKey
	}

	result := &DeviceImportResult{DryRun: dryRun, Rows: make([]*DeviceImportRowResult, len(rows))}

	// Pure checks and in-file duplicates. A rejected row claims no slot, so a later valid row
	// is not blamed for it.
	pending := make([]*importRow, 0, len(rows))
	seenToken := map[string]int{}
	seenExternal := map[string]int{}
	seenCred := map[string]int{}
	for i, row := range rows {
		outcome, code, message, p := validateImportRow(row, mode)
		if outcome != "" {
			result.set(i, row, outcome, code, message)
			continue
		}
		p.idx = i
		if first, dup := seenToken[row.Token]; dup {
			result.set(i, row, DeviceImportInvalid, importCodeDuplicateInFile, fmt.Sprintf("token also appears at row %d", first+1))
			continue
		}
		if p.external != "" {
			if first, dup := seenExternal[p.external]; dup {
				result.set(i, row, DeviceImportInvalid, importCodeDuplicateInFile, fmt.Sprintf("externalId also appears at row %d", first+1))
				continue
			}
		}
		credKey := p.credType + "\x00" + p.credID
		if p.credID != "" {
			if first, dup := seenCred[credKey]; dup {
				result.set(i, row, DeviceImportInvalid, importCodeDuplicateInFile, fmt.Sprintf("credentialId also appears at row %d", first+1))
				continue
			}
		}
		seenToken[row.Token] = i
		if p.external != "" {
			seenExternal[p.external] = i
		}
		if p.credID != "" {
			seenCred[credKey] = i
		}
		pending = append(pending, p)
	}

	if len(pending) > 0 {
		if err := api.decideImportRows(ctx, result, pending, mode, dryRun); err != nil {
			return nil, err
		}
	}
	result.tally()
	return result, nil
}

// importStoredCredentialId is the stored credential_id of an import row's credential.
func (api *Api) importStoredCredentialId(ctx context.Context, p *importRow) (string, error) {
	tenant, err := ctxTenant(ctx)
	if err != nil {
		return "", err
	}
	return storedCredentialId(api.DeviceSecretKey, tenant, p.credType, p.credID)
}

// decideImportRows is the database half: it reads what the pending rows could collide with
// in a handful of batched queries, decides every row, and (unless dryRun) writes the rows
// that are new, one transaction each.
func (api *Api) decideImportRows(ctx context.Context, result *DeviceImportResult, pending []*importRow, mode string, dryRun bool) error {
	typeTokens := make([]string, 0)
	tokens := make([]string, 0, len(pending))
	externals := make([]string, 0)
	seenType := map[string]bool{}
	credIDs := map[string][]string{}
	for _, p := range pending {
		t := strings.TrimSpace(p.row.DeviceTypeToken)
		if !seenType[t] {
			seenType[t] = true
			typeTokens = append(typeTokens, t)
		}
		tokens = append(tokens, p.row.Token)
		if p.external != "" {
			externals = append(externals, p.external)
		}
		if p.credID != "" {
			stored, err := api.importStoredCredentialId(ctx, p)
			if err != nil {
				return err
			}
			p.credStored = stored
			credIDs[p.credType] = append(credIDs[p.credType], stored)
		}
	}

	types := map[string]*DeviceType{}
	typeRows, err := api.DeviceTypesByToken(ctx, typeTokens)
	if err != nil {
		return err
	}
	for _, dt := range typeRows {
		types[dt.Token] = dt
	}
	byToken := map[string]*Device{}
	existing, err := api.DevicesByToken(ctx, tokens)
	if err != nil {
		return err
	}
	for _, d := range existing {
		byToken[d.Token] = d
	}
	byExternal := map[string]*Device{}
	if len(externals) > 0 {
		found, err := api.DevicesByExternalId(ctx, externals)
		if err != nil {
			return err
		}
		for _, d := range found {
			byExternal[d.ExternalId.String] = d
		}
	}
	// credential (type, id) -> owning device id.
	credOwner := map[string]uint{}
	for ctype, ids := range credIDs {
		var found []*DeviceCredential
		if err := rdb.FindByKeys(api.RDB.DB(ctx).Where("credential_type = ?", ctype), &found, "credential_id", ids); err != nil {
			return err
		}
		for _, c := range found {
			credOwner[ctype+"\x00"+c.CredentialId] = c.DeviceId
		}
	}
	created := make([]*Device, 0)
	for _, p := range pending {
		row := p.row
		i := p.idx
		dt := types[strings.TrimSpace(row.DeviceTypeToken)]
		credKey := p.credType + "\x00" + p.credStored

		if dev := byToken[row.Token]; dev != nil {
			if diff := differingFields(dev, p); len(diff) > 0 {
				result.set(i, row, DeviceImportConflict, conflict.Code,
					"a device with this token exists with a different "+strings.Join(diff, ", ")+"; an import never updates an existing device")
				continue
			}
			if mode == DeviceImportModeCredentials {
				owner, has := credOwner[credKey]
				switch {
				case !has:
					result.set(i, row, DeviceImportConflict, conflict.Code,
						"the device exists but has no credential with this credentialId; an import never modifies an existing device")
					continue
				case owner != dev.ID:
					result.set(i, row, DeviceImportConflict, conflict.Code, "credentialId is already in use by another device")
					continue
				}
			}
			result.set(i, row, DeviceImportExists, "", "")
			continue
		}
		if dt == nil {
			result.set(i, row, DeviceImportInvalid, importCodeNotFound, fmt.Sprintf("device type %q not found", strings.TrimSpace(row.DeviceTypeToken)))
			continue
		}
		if p.external != "" && byExternal[p.external] != nil {
			result.set(i, row, DeviceImportConflict, conflict.Code, "externalId is already in use by another device")
			continue
		}
		if mode == DeviceImportModeCredentials {
			if _, taken := credOwner[credKey]; taken {
				result.set(i, row, DeviceImportConflict, conflict.Code, "credentialId is already in use")
				continue
			}
		}
		if dryRun {
			result.set(i, row, DeviceImportCreated, "", "")
			continue
		}

		device, err := api.createImportedDevice(ctx, p, dt, mode)
		switch {
		case err == nil:
			created = append(created, device)
			result.set(i, row, DeviceImportCreated, "", "")
		case conflict.Is(err):
			// A unique value collided at the write: a concurrent writer, or one the reads above
			// do not cover (the derived credential token).
			result.set(i, row, DeviceImportConflict, conflict.Code, "a value that must be unique (a token, external id or credential id) is already in use")
		default:
			api.emitDeviceRosterBatch(ctx, created)
			return fmt.Errorf("import stopped at row %d after creating %d devices; re-run the same file and the created rows report EXISTS: %w",
				i+1, len(created), err)
		}
	}
	api.emitDeviceRosterBatch(ctx, created)
	return nil
}

// createImportedDevice writes one row: the device and, in credentials mode, its credential,
// in one transaction.
func (api *Api) createImportedDevice(ctx context.Context, p *importRow, dt *DeviceType, mode string) (*Device, error) {
	device := &Device{
		TokenReference:    rdb.TokenReference{Token: p.row.Token},
		ExternalReference: rdb.ExternalReference{ExternalId: sqlnull.Text(p.row.ExternalId)},
		NamedEntity: rdb.NamedEntity{
			Name:        sqlnull.Text(p.row.Name),
			Description: sqlnull.Text(p.row.Description),
		},
		DeviceTypeId: dt.ID,
	}
	if p.metadata != nil {
		raw := json.RawMessage(*p.metadata)
		md := datatypes.JSON(raw)
		device.Metadata = &md
	}
	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(device).Error; err != nil {
			return err
		}
		if mode != DeviceImportModeCredentials {
			return nil
		}
		cred, err := buildDeviceCredential(api.DeviceSecretKey, device, &DeviceCredentialCreateRequest{
			Token:           p.row.Token + importCredentialTokenSuffix,
			DeviceToken:     p.row.Token,
			CredentialType:  p.credType,
			CredentialId:    p.credID,
			CredentialValue: p.row.CredentialValue,
			Enabled:         true,
		})
		if err != nil {
			return err
		}
		// The device is already written: insert the credential by its id, never through the
		// association (which would upsert the device row again).
		return tx.Omit("Device").Create(cred).Error
	})
	if err != nil {
		return nil, err
	}
	device.DeviceType = dt
	return device, nil
}
