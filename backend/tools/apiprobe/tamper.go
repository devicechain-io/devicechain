// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// tamper is the upgrade rig's NEGATIVE CONTROL, expressed as data.
//
// # WHY THE TOOL OWNS THIS AND NOT THE RIG
//
// The rule the HA rig and the restore drill are built around is that a check is
// worth nothing until it has been shown to fail. `apiprobe verify` passing after
// an upgrade means nothing on its own — a verify that always passed would look
// exactly the same. So the rig breaks the instance on purpose and requires
// verify to notice, with the EXACT exit code for the kind of damage it did.
//
// Breaking it is a GraphQL mutation against the tenant the receipt names, as the
// identity the receipt carries, addressed by the token the receipt recorded. All
// three of those live here, not in shell, and so does the coverage table that
// says how each entity is read back. A rig that hand-wrote the mutation would be
// keeping a second copy of the schema in bash.
//
// # ARMING IS ASSERTED, NOT ASSUMED
//
// 🔴 The trap this is built to avoid: a control that "held" because the tamper
// never happened. If the delete were silently refused, verify would pass, the
// rig would report THE CONTROL DID NOT HOLD, and the finding would read as "the
// probe cannot see a deleted row" when the truth is "nothing was deleted". Those
// two are indistinguishable from the rig's side.
//
// So tamper does not report success until it has read the entity back THROUGH
// THE COVERAGE TABLE'S OWN QUERY and confirmed the damage is visible there:
// missing for a delete, and a canonically different object for a modify — which
// is the very comparison verify makes. Anything else exits inconclusive, and
// says the control could not be armed rather than pretending it was.
type tamper struct {
	// Mode is the --mode value, and names the KIND of damage: which exit code
	// the rig is proving verify can produce.
	Mode string

	// Entity names a row in the coverage table. The table supplies the area, the
	// read-back query and the selection, so the damage is asserted through
	// exactly the query verify will use.
	Entity string

	// Mutation is the field that does the damage, and Arg its argument name —
	// spelled as the schema spells them, and checked against it by a test.
	Mutation string
	Arg      string

	// Input is the request type for a modify, empty for a delete.
	Input string

	// Field is the field a modify rewrites, and Value what it writes.
	//
	// 🔑 Field MUST be one the entity SELECTS. Verify compares only the selected
	// fields, so rewriting an unselected one would leave the read-back identical
	// and the control asserting nothing — the exact silent-pass this whole tool
	// exists to refuse. A test pins it against the entity's own selection.
	Field string
	Value string
}

const (
	tamperDelete = "delete"
	tamperModify = "modify"
)

// querier is the one thing tamper needs from a session, named so the arming
// assertion can be exercised against a stub. Satisfied by userclient.TenantSession.
type querier interface {
	Query(ctx context.Context, baseURL, query string, variables map[string]any, out any) error
}

// The two controls, and their ORDER IN THIS TABLE IS LOAD-BEARING.
//
// 🔴 The rig runs `delete` first and `modify` second, and cannot run them the
// other way round. Verify walks the receipt in table order and stops at the
// FIRST entity that fails, so once the modify is applied every later failure is
// invisible behind it. Delete-then-modify works because asset-type precedes
// asset: the delete alone is seen at `asset`, and the modify is then seen at
// `asset-type` before the run ever reaches the hole.
//
// Reversed, the second control would report MISSING where the rig demanded
// MISMATCH — a loud failure rather than a silent one, but a confusing one. The
// ordering is asserted by a test rather than left to this comment.
//
// Both targets are LEAVES of the seed graph. Nothing later in the coverage table
// references the probe asset, so deleting it cannot cascade into a second
// entity and turn a one-row control into a multi-row one; and rewriting the
// asset type's own fields leaves the asset's reference to it intact.
var tampers = []tamper{
	{
		// deleteAsset is a real delete, so the row stops being returned by
		// assetsByToken — which is the signature of the failure this drill
		// exists to catch: a migration that dropped data while every schema-only
		// diff stayed green.
		Mode:     tamperDelete,
		Entity:   "asset",
		Mutation: "deleteAsset",
		Arg:      "token",
	},
	{
		// updateAssetType takes the CREATE request type, and the platform's
		// updates are still full-replace — so a request carrying only the token
		// and a new name also clears the description, the branding fields and
		// the metadata. That makes the damage larger than the one field named
		// here, which is fine: the control needs the object to DIFFER, and a
		// bigger difference is not a weaker one. `name` is named because it is
		// the field this deliberately rewrites; the rest is the platform's
		// update semantics showing through, and worth seeing in the output.
		Mode:     tamperModify,
		Entity:   "asset-type",
		Mutation: "updateAssetType",
		Arg:      "token",
		Input:    "AssetTypeCreateRequest!",
		Field:    "name",
		Value:    "TAMPERED BY THE UPGRADE RIG",
	},
}

func tamperFor(mode string) (tamper, bool) {
	for _, t := range tampers {
		if t.Mode == mode {
			return t, true
		}
	}
	return tamper{}, false
}

func tamperModes() string {
	modes := make([]string, 0, len(tampers))
	for _, t := range tampers {
		modes = append(modes, t.Mode)
	}
	return strings.Join(modes, " | ")
}

// doc renders the mutation. A delete is addressed by token alone; a modify
// carries the request as a second variable.
//
// The request is built from the token plus the one field, and the token is in it
// because every create request on this table declares `token: String!` — an
// update that reuses the create input has to supply it or be rejected for a
// reason that has nothing to do with the control.
func (t tamper) doc() string {
	if t.Input == "" {
		return "mutation($token:String!){" + t.Mutation + "(" + t.Arg + ":$token)}"
	}
	return "mutation($token:String!,$req:" + t.Input + "){" +
		t.Mutation + "(" + t.Arg + ":$token,request:$req){token}}"
}

func (t tamper) vars(token string) map[string]any {
	if t.Input == "" {
		return map[string]any{"token": token}
	}
	return map[string]any{
		"token": token,
		"req":   map[string]any{"token": token, t.Field: t.Value},
	}
}

func runTamper(ctx context.Context, argv []string) error {
	fs := flagSetFor("tamper")
	var c connection
	c.bind(fs)
	var receipt, mode string
	fs.StringVar(&receipt, "receipt", "", "path to the receipt seed wrote (required)")
	fs.StringVar(&mode, "mode", "", "the damage to do: "+tamperModes()+" (required)")
	if err := fs.Parse(argv); err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if strings.TrimSpace(receipt) == "" {
		return failWith(exitSetup, "--receipt is required")
	}
	t, ok := tamperFor(mode)
	if !ok {
		return failWith(exitSetup, "unknown --mode %q; try one of: %s", mode, tamperModes())
	}

	rec, err := readReceipt(receipt)
	if err != nil {
		return err
	}
	// Same reason verify does it: the receipt names the tenant, and tampering
	// with the wrong one would arm nothing while reporting that it had.
	c.tenant = rec.Tenant
	session := c.session(rec.Identity)

	e, ok := entityNamed(t.Entity)
	if !ok {
		return failWith(exitSetup, "the %s control targets %q, which is not in the coverage table", t.Mode, t.Entity)
	}
	r, ok := recordedNamed(rec, t.Entity)
	if !ok {
		return failWith(exitSetup, "the receipt records no %q; the seed this control needs did not happen", t.Entity)
	}

	var envelope map[string]json.RawMessage
	if err := session.Query(ctx, c.areaURL(e.Area), t.doc(), t.vars(r.Token), &envelope); err != nil {
		return failWith(exitSetup, "%s %s %q: %w", t.Mode, t.Entity, r.Token, err)
	}
	if _, ok := envelope[t.Mutation]; !ok {
		return failWith(exitSetup, "%s returned no %q key; the control cannot be armed against a schema it no longer matches",
			t.Mode, t.Mutation)
	}
	if err := deleteLanded(t, envelope[t.Mutation], r.Token); err != nil {
		return err
	}

	return confirmArmed(ctx, &c, session, e, r, t)
}

// deleteLanded reads the answer a delete gave.
//
// 🔑 deleteX returns Boolean!, and `false` is the API DECLINING rather than
// failing — a referenced row, a permission, a guard. That is the one outcome
// that must never be reported as an armed control: the row is still there, so
// verify will pass, and the rig would announce that the probe cannot see a
// deleted row when nothing was ever deleted.
//
// A modify has no such answer to read, and returns nil unexamined.
func deleteLanded(t tamper, answer json.RawMessage, token string) error {
	if t.Mode != tamperDelete {
		return nil
	}
	var deleted bool
	if err := json.Unmarshal(answer, &deleted); err != nil {
		return failWith(exitSetup, "%s did not answer with a boolean: %w", t.Mutation, err)
	}
	if !deleted {
		return failWith(exitSetup, "%s(%s: %q) returned false: the row was NOT deleted, so there is no control to run",
			t.Mutation, t.Arg, token)
	}
	return nil
}

// confirmArmed re-reads the entity through the coverage table's own query and
// requires the damage to be visible there.
//
// 🔑 This is the premise assertion, and it is the whole reason this subcommand
// exists rather than the rig calling the mutation itself. It uses readObject —
// the same function verify uses, reached through the same document — so "armed"
// means precisely "verify will now fail", not "a mutation returned without an
// error".
func confirmArmed(ctx context.Context, c *connection, session querier, e entity, r Recorded, t tamper) error {
	var envelope map[string]json.RawMessage
	if err := session.Query(ctx, c.areaURL(e.Area), e.readDoc(), map[string]any{"token": r.Token}, &envelope); err != nil {
		return failWith(exitSetup, "reading %s %q back to confirm the control is armed: %w", t.Entity, r.Token, err)
	}

	found, readErr := readObject(e, envelope, r.Token)

	if t.Mode == tamperDelete {
		if readErr == nil {
			return failWith(exitSetup, "%s %q is STILL THERE after %s; the control was not armed and a verify that passes now proves nothing",
				t.Entity, r.Token, t.Mutation)
		}
		if code := codeOf(readErr); code != exitMissing {
			// The read failed for some OTHER reason — a schema change, an
			// unparseable response. That is not the damage this control does, and
			// calling it armed would let verify's later exit be attributed to a
			// delete that may never have landed.
			return failWith(exitSetup, "after %s, reading %s %q back failed with exit %d rather than reporting it missing: %w",
				t.Mutation, t.Entity, r.Token, code, readErr)
		}
		fmt.Printf("armed: %s %q is deleted and no longer reads back.\n", t.Entity, r.Token)
		return nil
	}

	if readErr != nil {
		return failWith(exitSetup, "after %s, %s %q could not be read back at all: %w",
			t.Mutation, t.Entity, r.Token, readErr)
	}
	// The SAME comparison verify makes, for the same reason: an update the
	// platform accepted and then normalised back to the original value would
	// leave verify green, and only this comparison can tell that apart from a
	// verify that cannot see a change.
	want, err := canonical(r.Object)
	if err != nil {
		return failWith(exitSetup, "receipt entry %s is unreadable: %w", t.Entity, err)
	}
	got, err := canonical(found)
	if err != nil {
		return failWith(exitSetup, "the re-read %s is unparseable: %w", t.Entity, err)
	}
	if want == got {
		return failWith(exitSetup, "%s %q is UNCHANGED after %s set %s=%q; the control was not armed:\n   %s",
			t.Entity, r.Token, t.Mutation, t.Field, t.Value, got)
	}
	fmt.Printf("armed: %s %q now differs from what was recorded.\n   recorded: %s\n   now:      %s\n",
		t.Entity, r.Token, want, got)
	return nil
}

func entityNamed(name string) (entity, bool) {
	for _, e := range entities {
		if e.Name == name {
			return e, true
		}
	}
	return entity{}, false
}

func recordedNamed(rec Receipt, name string) (Recorded, bool) {
	for _, r := range rec.Entities {
		if r.Name == name {
			return r, true
		}
	}
	return Recorded{}, false
}
