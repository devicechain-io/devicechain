// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrCredentialIdRequired is returned when a replacement asks for a credential type
// the server cannot invent an identifier for. Only ACCESS_TOKEN is mintable — its id
// IS the bearer, so a random UUID is a complete credential — while X509_CERTIFICATE
// needs a CA-signed thumbprint and MQTT_BASIC a chosen username. It is a sentinel so
// a caller can distinguish "you forgot the id" from "that type does not exist",
// which are different operator mistakes with different fixes.
//
// The same distinction already exists on the provisioning path
// (provisionableCredentialType); this is the authenticated-operator analogue, where
// supplying the material is possible and so the answer is "supply it" rather than
// "unsupported".
var ErrCredentialIdRequired = errors.New("credentialId is required for this credential type: only ACCESS_TOKEN can be minted server-side")

// ReplaceDevice binds a NEW physical unit to an EXISTING logical device identity
// (ADR-074): it retires every credential the outgoing unit could authenticate with,
// mints one for the incoming unit, and writes an append-only DeviceReplacement
// record of the swap. All three happen in one transaction.
//
// # What carries forward, and why nothing has to be moved
//
// Events, alarms, relationship edges, group memberships and the command vocabulary
// all key on the DEVICE — its row id, or its token across a service seam (ADR-044) —
// and this operation changes neither. So "history carries forward" is not something
// this function does; it is something it is careful NOT to break. The whole design
// of ADR-014 (identity stable, credentials rotatable) is what makes that true, and
// DeviceReplaceRequest carrying no identity field is what keeps it true.
//
// # The retirement is the security-critical half
//
// Minting a credential for the new unit is the obvious half and the harmless one.
// DISABLING the outgoing ones is what this operation is FOR: a failed unit is
// commonly still powered, still in someone's hands, or on a bench — and until its
// credential is disabled it authenticates exactly as it always did, under the
// identity now also held by its replacement. Two units answering as one device
// produces telemetry no reader can attribute and a device-state projection that
// flaps between them.
//
// 🔴 THAT IS ALSO WHY THIS IS NOT "createDeviceCredential, then remember to disable
// the old ones". Those are two mutations with nothing joining them; the second is
// the one an operator under pressure skips, and skipping it leaves a live duplicate
// with no signal that anything is wrong. Here the retirement and the mint are one
// transaction: either the swap happened or it did not.
//
// Credentials are DISABLED, not deleted. The row survives so the historical binding
// stays readable and the replacement record's RetiredCredentialTokens resolve to
// something. DeviceCredentialByCredentialId — the resolve every transport
// authenticates through — matches `enabled = true` only, so a retired credential
// authenticates nothing.
//
// 🔴 THAT STOPS RECONNECTION, NOT THE SESSION ALREADY IN FLIGHT, and the difference
// matters to whoever is standing at the device. Nothing here evicts a connected
// client: a retired unit that is still powered and still holding its session keeps
// publishing until it drops or its device JWT expires (12h). What is guaranteed is
// that it can never come back. An operator who needs the old unit silent NOW has to
// power it down; the platform will not do it for them.
//
// # Consequences a caller should know
//
//   - A device with no live credential is replaced successfully, retiring nothing.
//     A unit that died before it ever provisioned is exactly the case the operation
//     exists for, so refusing it would be backwards.
//   - The retired credential ids stay OCCUPIED. The live-rows unique index over
//     (tenant_id, credential_type, credential_id) does not consider `enabled`, so
//     reusing the outgoing unit's credential id for the incoming one is refused by
//     the database. That is the correct answer — reuse would make the two units
//     indistinguishable at authentication, which is the thing being prevented — and
//     it fails loudly rather than silently rebinding.
//   - now is supplied by the caller so the recorded instant is deterministic in
//     tests. It is the SERVER's clock in production, never a request field.
//
// Concurrent replacements of the same device are serialized by a FOR UPDATE lock on
// the device row, taken at the top of the transaction — see the comment there for
// what that buys and what the sqlite harness can and cannot show about it.
func (api *Api) ReplaceDevice(ctx context.Context, request *DeviceReplaceRequest,
	actor string, now time.Time) (*DeviceReplaceResult, error) {

	devices, err := api.DevicesByToken(ctx, []string{request.DeviceToken})
	if err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return nil, fmt.Errorf("%w: device %q", gorm.ErrRecordNotFound, request.DeviceToken)
	}
	device := devices[0]

	credentialRequest, err := replacementCredentialRequest(request, device.Token)
	if err != nil {
		return nil, err
	}
	// Validate and render the new credential BEFORE the transaction opens, so a bad
	// credential type or an unparseable expiry refuses the whole replacement without
	// having retired anything.
	credential, err := buildDeviceCredential(device, credentialRequest)
	if err != nil {
		return nil, err
	}

	// The record is assembled here but its RetiredCredentialTokens are filled INSIDE
	// the transaction, from the same read that decides which rows to disable. Reading
	// the credentials out here and disabling them in there would let a credential
	// minted between the two reads survive the swap, still enabled and absent from
	// the record that claims to list everything this replacement retired.
	replacement := &DeviceReplacement{
		DeviceId:           device.ID,
		OccurredTime:       now,
		Actor:              actor,
		Reason:             rdb.NullStrOf(request.Reason),
		UnitIdentifier:     rdb.NullStrOf(request.UnitIdentifier),
		NewCredentialToken: credential.Token,
		NewCredentialType:  credential.CredentialType,
	}

	var retired []*DeviceCredential
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		// 🔴 SERIALIZE ON THE DEVICE ROW. This is what makes the operation's one
		// security property hold under concurrency: without it, two replacements of the
		// same device both read the same enabled set, both disable it, and each mints
		// its own credential — leaving TWO live units answering as one device, which is
		// precisely the outcome the whole operation exists to prevent.
		//
		// With the lock, the second transaction blocks here; under READ COMMITTED its
		// subsequent `enabled = true` read then runs on a fresh statement snapshot and
		// sees the credential the first one minted, so it retires that too. Exactly one
		// live credential, both swaps journalled.
		//
		// It is a LOCK, not a write: the devices row is read FOR UPDATE and never
		// modified, so this does not violate the promise that a replacement leaves the
		// identity untouched.
		//
		// 🔴 THE SQLITE HARNESS CANNOT MEASURE THIS, and that is a property of locks
		// rather than a gap to be closed here. glebarez/sqlite registers a "FOR" clause
		// builder that silently DROPS clause.Locking — the statement renders as a plain
		// SELECT and returns no error — so the model tests exercise the read and prove
		// nothing about the exclusion. On Postgres the identical line renders FOR UPDATE.
		// Stated so the next reader does not mistake a green test suite for evidence
		// that the race is covered.
		var locked Device
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", device.ID).First(&locked).Error; err != nil {
			return err
		}

		// Every live, enabled credential this device holds — unbounded on purpose (the
		// explicit internal unbounded path, ADR-029). A page bound here would leave
		// whatever fell past the page still able to authenticate, which is precisely
		// the outcome this operation exists to prevent, and it would do it silently.
		retired = retired[:0]
		if err := tx.Where("device_id = ? AND enabled = ?", device.ID, true).
			Order("id ASC").Find(&retired).Error; err != nil {
			return err
		}

		if len(retired) > 0 {
			ids := make([]uint, 0, len(retired))
			for _, cred := range retired {
				ids = append(ids, cred.ID)
			}
			// Disable exactly the rows that were read, by id, so the record's retired
			// list and the rows actually retired cannot disagree.
			if err := tx.Model(&DeviceCredential{}).Where("id IN ?", ids).
				Update("enabled", false).Error; err != nil {
				return err
			}
			for _, cred := range retired {
				cred.Enabled = false
			}
		}

		tokens := make([]string, 0, len(retired))
		for _, cred := range retired {
			tokens = append(tokens, cred.Token)
		}
		encoded, err := json.Marshal(tokens)
		if err != nil {
			return err
		}
		replacement.RetiredCredentialTokens = encoded

		// Omit the Device association: the row already carries DeviceId, and letting
		// gorm write through the association would issue an upsert against the devices
		// table — a write to the identity this operation promises not to touch.
		if err := tx.Omit("Device").Create(credential).Error; err != nil {
			return err
		}
		return tx.Omit("Device").Create(replacement).Error
	})
	if err != nil {
		return nil, err
	}

	return &DeviceReplaceResult{
		Device:             device,
		Replacement:        replacement,
		NewCredential:      credential,
		RetiredCredentials: retired,
	}, nil
}

// replacementCredentialRequest folds the replacement's optional credential fields
// into the create request shape, applying the two defaults and the one refusal.
//
// Defaults: the type is ACCESS_TOKEN (matching provisioning), and the entity token
// is a fresh UUID (mintOrReuseCredential does the same — an operator has no reason
// to name a credential row).
//
// The refusal is the interesting one. An ACCESS_TOKEN's CredentialId is itself the
// bearer secret, so a random UUID is a complete, usable credential and minting one
// is right. For every other type the id is material that exists outside this system
// — a certificate thumbprint, an MQTT username — and inventing one would produce a
// credential that authenticates nothing while reporting success. That is the
// "unimplemented must fail loudly" line: a plausible value is worse than an error.
//
// The new credential is always ENABLED. A disabled one would leave the device with
// no way to authenticate at all, which is a worse outcome than the state the
// replacement was called to fix.
func replacementCredentialRequest(request *DeviceReplaceRequest, deviceToken string) (*DeviceCredentialCreateRequest, error) {
	credentialType := string(CredentialAccessToken)
	if request.CredentialType != nil {
		credentialType = *request.CredentialType
	}

	credentialId := ""
	switch {
	case request.CredentialId != nil && *request.CredentialId != "":
		credentialId = *request.CredentialId
	case CredentialType(credentialType) == CredentialAccessToken:
		credentialId = uuid.New().String()
	default:
		// An unknown type falls through here too, and that is fine: it reaches
		// buildDeviceCredential's vocabulary check either way. Only report the missing
		// id for a type that actually exists, so an operator who mistyped the TYPE is
		// not told to supply an id for it.
		if CredentialType(credentialType).Valid() {
			return nil, fmt.Errorf("%w (%s)", ErrCredentialIdRequired, credentialType)
		}
	}

	credentialToken := uuid.New().String()
	if request.CredentialToken != nil && *request.CredentialToken != "" {
		credentialToken = *request.CredentialToken
	}

	return &DeviceCredentialCreateRequest{
		Token:           credentialToken,
		DeviceToken:     deviceToken,
		CredentialType:  credentialType,
		CredentialId:    credentialId,
		CredentialValue: request.CredentialValue,
		Enabled:         true,
		ExpiresAt:       request.ExpiresAt,
	}, nil
}

// DeviceReplacements lists replacement records (ADR-074), newest first, optionally
// narrowed to one device. It is a read over an append-only journal, so there is no
// by-id or by-token door to go with it: a single replacement is never addressed on
// its own, only read as part of a device's history.
func (api *Api) DeviceReplacements(ctx context.Context,
	criteria DeviceReplacementSearchCriteria) (*DeviceReplacementSearchResults, error) {

	results := make([]DeviceReplacement, 0)
	db, pag := api.RDB.ListOf(ctx, &DeviceReplacement{}, func(result *gorm.DB) *gorm.DB {
		if criteria.Device != nil {
			result = result.Where("device_id = (?)",
				api.RDB.DB(ctx).Model(&Device{}).Select("id").Where("token = ?", criteria.Device))
		}
		return result.Preload("Device")
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	return &DeviceReplacementSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// RetiredCredentialTokenList decodes the stored JSON array of retired credential
// tokens, and NEVER returns nil.
//
// 🔴 THE NEVER-NIL PART IS A CONTRACT, NOT A COURTESY, and it is enforced on every
// exit below rather than asserted here. The SDL field is `retiredCredentialTokens:
// [String!]!` — non-null — so a nil slice renders `null` for a non-null field, which
// errors the WHOLE query rather than that one field. A journal read failing wholesale
// because one row's annotation is unreadable is exactly the outcome to avoid.
//
// 🔴 THE THIRD EXIT IS THE ONE THAT WAS WRONG, AND A MUTANT IS WHAT FOUND IT. The
// stored bytes `null` are VALID JSON: Unmarshal succeeds and leaves the slice nil, so
// a guard covering only "no bytes" and "decode failed" let a nil out through the one
// path that reported no error at all. Nothing written by ReplaceDevice can be `null`
// — it always marshals at least `[]` — which is precisely why no test working through
// ReplaceDevice could reach it. See TestRetiredCredentialTokenListIsAlwaysASlice.
func (r DeviceReplacement) RetiredCredentialTokenList() []string {
	tokens := make([]string, 0)
	if len(r.RetiredCredentialTokens) == 0 {
		return tokens
	}
	if err := json.Unmarshal(r.RetiredCredentialTokens, &tokens); err != nil {
		return make([]string, 0)
	}
	// A SUCCESSFUL decode can still leave a nil slice: `null` unmarshals into one
	// without error, and so does any JSON whose top level is not an array once the
	// error branch above has been passed. This is the exit the never-nil contract is
	// actually enforced on.
	if tokens == nil {
		return make([]string, 0)
	}
	return tokens
}
