// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// stubQuerier answers one canned response per call, in order, so the arming
// assertion can be exercised without a cluster. It records the documents it was
// asked for, which is how the tests below tell a read from a mutation.
type stubQuerier struct {
	responses []string
	errs      []error
	docs      []string
	calls     int
}

func (s *stubQuerier) Query(_ context.Context, _, query string, _ map[string]any, out any) error {
	s.docs = append(s.docs, query)
	i := s.calls
	s.calls++
	if i < len(s.errs) && s.errs[i] != nil {
		return s.errs[i]
	}
	if i >= len(s.responses) {
		return errPlain("stub ran out of responses")
	}
	return json.Unmarshal([]byte(s.responses[i]), out)
}

func assetTypeEntity(t *testing.T) entity {
	t.Helper()
	e, ok := entityNamed("asset-type")
	if !ok {
		t.Fatal("the coverage table has no asset-type")
	}
	return e
}

func assetEntity(t *testing.T) entity {
	t.Helper()
	e, ok := entityNamed("asset")
	if !ok {
		t.Fatal("the coverage table has no asset")
	}
	return e
}

func mustTamper(t *testing.T, mode string) tamper {
	t.Helper()
	tm, ok := tamperFor(mode)
	if !ok {
		t.Fatalf("no %q control", mode)
	}
	return tm
}

// ---------------------------------------------------------------------------
// the table
// ---------------------------------------------------------------------------

// Both modes have to exist. The rig runs each by name, and a mode that quietly
// disappeared would make the rig fail with "unknown --mode" — which reads as a
// rig bug rather than as a control that is no longer being run.
func TestBothControlsExist(t *testing.T) {
	for _, mode := range []string{tamperDelete, tamperModify} {
		if _, ok := tamperFor(mode); !ok {
			t.Errorf("there is no %q control", mode)
		}
	}
	seen := map[string]bool{}
	for _, tm := range tampers {
		if seen[tm.Mode] {
			t.Errorf("two controls claim mode %q; --mode would silently run only the first", tm.Mode)
		}
		seen[tm.Mode] = true
	}
}

func TestEveryControlTargetsAnEntityInTheTable(t *testing.T) {
	for _, tm := range tampers {
		if _, ok := entityNamed(tm.Entity); !ok {
			t.Errorf("the %s control targets %q, which is not in the coverage table", tm.Mode, tm.Entity)
		}
		if tm.Mutation == "" || tm.Arg == "" {
			t.Errorf("the %s control is incomplete: mutation=%q arg=%q", tm.Mode, tm.Mutation, tm.Arg)
		}
	}
}

// 🔴 THE LOAD-BEARING ONE. Verify compares only the fields the entity SELECTS,
// so a modify that rewrites an unselected field would leave the read-back
// identical: verify would pass, and the rig would report the control as not
// holding while the truth is that nothing observable was changed. That is the
// silent-pass this whole tool exists to refuse, and it is invisible in a review
// because both the field and the selection are plausible on their own.
func TestAModifyRewritesAFieldTheEntitySelects(t *testing.T) {
	for _, tm := range tampers {
		if tm.Mode != tamperModify {
			continue
		}
		e, ok := entityNamed(tm.Entity)
		if !ok {
			continue // reported by the test above
		}
		if tm.Field == "" || tm.Value == "" {
			t.Errorf("the %s control on %q sets field=%q value=%q", tm.Mode, tm.Entity, tm.Field, tm.Value)
			continue
		}
		selected := false
		for _, f := range strings.Fields(e.Fields) {
			// The selection can carry nested sets like `assetType{token}`; the
			// leading name is the field.
			if name, _, _ := strings.Cut(f, "{"); name == tm.Field {
				selected = true
				break
			}
		}
		if !selected {
			t.Errorf("the %s control rewrites %q on %q, which the entity does not select (%q) — verify could not see it",
				tm.Mode, tm.Field, tm.Entity, e.Fields)
		}
	}
}

// 🔴 THE ORDERING INVARIANT, enforced rather than described. Verify stops at the
// FIRST entity that fails, so the rig can only run delete-then-modify if the
// modify's target comes EARLIER in the coverage table than the delete's: the
// delete alone is seen where the hole is, and the modify is then seen before the
// run ever reaches it. Reorder the table and the second control starts reporting
// MISSING where the rig demands MISMATCH.
func TestTheModifyTargetPrecedesTheDeleteTarget(t *testing.T) {
	index := map[string]int{}
	for i, e := range entities {
		index[e.Name] = i
	}
	del, mod := mustTamper(t, tamperDelete), mustTamper(t, tamperModify)
	if index[mod.Entity] >= index[del.Entity] {
		t.Errorf("the modify target %q is at %d and the delete target %q at %d; "+
			"the delete's hole would mask the modify, so verify would exit MISSING for both controls",
			mod.Entity, index[mod.Entity], del.Entity, index[del.Entity])
	}
}

// The deleted entity has to be a LEAF of the seed graph, or the control does
// more damage than it claims and the rig's MISSING finding could be about a
// cascade rather than about the row it deleted.
//
// 🔑 "Leaf" is exactly measurable here rather than a judgement call: an entity
// is referenced by a later one only through the shared state, and only entities
// with a Record function ever put anything into it. No Record ⇒ nothing can
// name it ⇒ nothing can cascade.
func TestTheDeleteTargetIsALeaf(t *testing.T) {
	del := mustTamper(t, tamperDelete)
	e, ok := entityNamed(del.Entity)
	if !ok {
		t.Fatalf("no entity %q", del.Entity)
	}
	if e.Record != nil {
		t.Errorf("the delete control targets %q, which records its token into the shared state — "+
			"a later entity may reference it, so deleting it could cascade beyond the one row the control claims",
			del.Entity)
	}
}

// The mutation names and argument spellings are read out of the served schema
// rather than remembered, for the same reason the coverage table's are: a
// renamed argument would make the control fail to arm, and "the control did not
// hold" is the most misleading way possible to report a typo.
func TestEveryControlNamesSomethingTheSchemaDeclares(t *testing.T) {
	for _, tm := range tampers {
		e, ok := entityNamed(tm.Entity)
		if !ok {
			continue
		}
		schema := stripSpace(schemaFor(t, e.Area))
		if !strings.Contains(schema, tm.Mutation+"("+tm.Arg+":") {
			t.Errorf("the %s control calls %s(%s:…), which %s does not declare", tm.Mode, tm.Mutation, tm.Arg, e.Area)
		}
		if tm.Input != "" {
			typeName := strings.TrimSuffix(tm.Input, "!")
			if !strings.Contains(schemaFor(t, e.Area), "input "+typeName+" {") {
				t.Errorf("the %s control sends %s, which %s does not declare", tm.Mode, typeName, e.Area)
			}
			// The request has to reach the mutation under the name the document
			// uses, and `request` is hard-coded in doc().
			if !strings.Contains(schema, tm.Mutation+"("+tm.Arg+":String!,request:") {
				t.Errorf("the %s control passes request:, which %s does not declare on %s", tm.Mode, e.Area, tm.Mutation)
			}
		}
	}
}

// A delete addresses the row and nothing else; a modify carries the request. The
// two documents are different shapes, and generating the wrong one produces a
// query error that would be reported as a control that could not be armed.
func TestTheControlDocumentsMatchTheirMode(t *testing.T) {
	del := mustTamper(t, tamperDelete)
	if got := del.doc(); strings.Contains(got, "request:") || strings.Contains(got, "$req") {
		t.Errorf("the delete document carries a request: %s", got)
	}
	if got := del.vars("tok"); len(got) != 1 || got["token"] != "tok" {
		t.Errorf("the delete variables are %v, want just the token", got)
	}

	mod := mustTamper(t, tamperModify)
	doc := mod.doc()
	if !strings.Contains(doc, "$req:"+mod.Input) || !strings.Contains(doc, "request:$req") {
		t.Errorf("the modify document does not pass its request: %s", doc)
	}
	req, _ := mod.vars("tok")["req"].(map[string]any)
	if req == nil || req["token"] != "tok" || req[mod.Field] != mod.Value {
		t.Errorf("the modify request is %v, want the token plus %s=%q", req, mod.Field, mod.Value)
	}
}

// ---------------------------------------------------------------------------
// arming
// ---------------------------------------------------------------------------

// 🔴 THE CONTROL ON THE CONTROL. A delete that was refused, ignored, or applied
// to the wrong tenant leaves the row exactly where it was — and then verify
// passes, the rig reports THE CONTROL DID NOT HOLD, and the finding reads as
// "the probe cannot see a deleted row" when nothing was ever deleted. From the
// rig's side those two are indistinguishable, so tamper has to tell them apart.
func TestADeleteThatLeftTheRowIsNotArmed(t *testing.T) {
	e := assetEntity(t)
	stub := &stubQuerier{responses: []string{`{"assetsByToken":[{"token":"t","name":"apiprobe asset"}]}`}}
	err := confirmArmed(context.Background(), &connection{}, stub, e,
		Recorded{Name: "asset", Token: "t", Object: raw(`{"token":"t","name":"apiprobe asset"}`)},
		mustTamper(t, tamperDelete))
	assertCode(t, err, exitSetup)
	if err == nil || !strings.Contains(err.Error(), "STILL THERE") {
		t.Errorf("the message does not say the row survived: %v", err)
	}
}

func TestADeleteThatRemovedTheRowIsArmed(t *testing.T) {
	e := assetEntity(t)
	stub := &stubQuerier{responses: []string{`{"assetsByToken":[]}`}}
	if err := confirmArmed(context.Background(), &connection{}, stub, e,
		Recorded{Name: "asset", Token: "t", Object: raw(`{"token":"t"}`)},
		mustTamper(t, tamperDelete)); err != nil {
		t.Errorf("an emptied read-back was not accepted as armed: %v", err)
	}
	// It has to arm through the coverage table's own query, not a query of its
	// own — that equivalence is the reason "armed" means "verify will now fail".
	if len(stub.docs) != 1 || stub.docs[0] != e.readDoc() {
		t.Errorf("arming used %q, not the entity's read document", stub.docs)
	}
}

// A read that failed for some OTHER reason — the schema moved, the response was
// unparseable — is not the damage this control does. Calling it armed would let
// verify's later exit be attributed to a delete that may never have landed.
func TestADeleteWhoseReadBreaksForAnotherReasonIsNotArmed(t *testing.T) {
	e := assetEntity(t)
	stub := &stubQuerier{responses: []string{`{"somethingElse":[]}`}}
	err := confirmArmed(context.Background(), &connection{}, stub, e,
		Recorded{Name: "asset", Token: "t", Object: raw(`{"token":"t"}`)},
		mustTamper(t, tamperDelete))
	assertCode(t, err, exitSetup)
	if err == nil || !strings.Contains(err.Error(), "rather than reporting it missing") {
		t.Errorf("the message does not distinguish a shape change from a delete: %v", err)
	}
}

// The mirror of the delete case: an update the platform accepted and then
// normalised back to the same value changes nothing observable, and a control
// that reports itself armed on that would be asserting nothing.
func TestAModifyThatChangedNothingIsNotArmed(t *testing.T) {
	e := assetTypeEntity(t)
	object := `{"token":"t","name":"apiprobe asset type"}`
	stub := &stubQuerier{responses: []string{`{"assetTypesByToken":[` + object + `]}`}}
	err := confirmArmed(context.Background(), &connection{}, stub, e,
		Recorded{Name: "asset-type", Token: "t", Object: raw(object)},
		mustTamper(t, tamperModify))
	assertCode(t, err, exitSetup)
	if err == nil || !strings.Contains(err.Error(), "UNCHANGED") {
		t.Errorf("the message does not say the object was unchanged: %v", err)
	}
}

func TestAModifyThatChangedTheObjectIsArmed(t *testing.T) {
	e := assetTypeEntity(t)
	stub := &stubQuerier{responses: []string{`{"assetTypesByToken":[{"token":"t","name":"TAMPERED BY THE UPGRADE RIG"}]}`}}
	if err := confirmArmed(context.Background(), &connection{}, stub, e,
		Recorded{Name: "asset-type", Token: "t", Object: raw(`{"token":"t","name":"apiprobe asset type"}`)},
		mustTamper(t, tamperModify)); err != nil {
		t.Errorf("a changed read-back was not accepted as armed: %v", err)
	}
}

// 🔑 THE COUNTERWEIGHT to the jsonb normalising in canonical(): arming compares
// the same way verify does, so a difference that is only rendering must NOT
// count as armed. Otherwise the modify control would report itself armed against
// an instance where the update did nothing at all.
func TestAModifyIsNotArmedByRenderingAlone(t *testing.T) {
	e := assetTypeEntity(t)
	stub := &stubQuerier{responses: []string{`{"assetTypesByToken":[{"name":"n","token":"t","metadata":"{\"probe\": \"asset-type\"}"}]}`}}
	err := confirmArmed(context.Background(), &connection{}, stub, e,
		Recorded{Name: "asset-type", Token: "t", Object: raw(`{"token":"t","name":"n","metadata":"{\"probe\":\"asset-type\"}"}`)},
		mustTamper(t, tamperModify))
	assertCode(t, err, exitSetup)
}

// A modify whose row vanished is not a modify control. Reporting it as armed
// would let the rig demand MISMATCH from a verify that will correctly say
// MISSING, and the mismatch half would never actually be exercised again.
func TestAModifyWhoseRowVanishedIsNotArmed(t *testing.T) {
	e := assetTypeEntity(t)
	stub := &stubQuerier{responses: []string{`{"assetTypesByToken":[]}`}}
	err := confirmArmed(context.Background(), &connection{}, stub, e,
		Recorded{Name: "asset-type", Token: "t", Object: raw(`{"token":"t"}`)},
		mustTamper(t, tamperModify))
	assertCode(t, err, exitSetup)
	if err == nil || !strings.Contains(err.Error(), "could not be read back at all") {
		t.Errorf("the message does not say the row was gone: %v", err)
	}
}

// ⚠️ Arming is never a verdict about the DATA. tamper reports only whether the
// control could be set up, so every failure here is inconclusive — a tamper that
// exited MISSING or MISMATCH would be indistinguishable from verify's own
// finding to any rig reading exit codes.
func TestArmingFailureIsAlwaysInconclusive(t *testing.T) {
	e := assetEntity(t)
	cases := []struct{ name, response string }{
		{"the row survived a delete", `{"assetsByToken":[{"token":"t"}]}`},
		{"the read key vanished", `{"nope":[]}`},
		{"the list is no longer a list", `{"assetsByToken":{"token":"t"}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stub := &stubQuerier{responses: []string{c.response}}
			err := confirmArmed(context.Background(), &connection{}, stub, e,
				Recorded{Name: "asset", Token: "t", Object: raw(`{"token":"t"}`)},
				mustTamper(t, tamperDelete))
			assertCode(t, err, exitSetup)
		})
	}
	// And the transport failing is inconclusive too, rather than "the row is
	// gone" — a dead ingress must never read as data loss.
	stub := &stubQuerier{errs: []error{errPlain("connection refused")}}
	assertCode(t, confirmArmed(context.Background(), &connection{}, stub, e,
		Recorded{Name: "asset", Token: "t", Object: raw(`{"token":"t"}`)},
		mustTamper(t, tamperDelete)), exitSetup)
}

// An unknown --mode has to be rejected by name rather than falling through to a
// no-op, or a rig with a typo would run no control at all and report success.
func TestAnUnknownModeIsRejected(t *testing.T) {
	if _, ok := tamperFor("scramble"); ok {
		t.Error("an unknown mode resolved to a control")
	}
	if modes := tamperModes(); !strings.Contains(modes, tamperDelete) || !strings.Contains(modes, tamperModify) {
		t.Errorf("the usage line lists %q, which does not name both controls", modes)
	}
}

// 🔴 `false` from a delete is the API DECLINING, not failing — a referenced row,
// a guard, a permission. The row is then still there, verify passes, and the rig
// announces that the probe cannot see a deleted row when nothing was deleted.
// This is the single most likely way for the delete control to lie.
func TestADeleteThatReturnedFalseIsRefused(t *testing.T) {
	del := mustTamper(t, tamperDelete)
	err := deleteLanded(del, raw(`false`), "t")
	assertCode(t, err, exitSetup)
	if err == nil || !strings.Contains(err.Error(), "was NOT deleted") {
		t.Errorf("the message does not say the row survived: %v", err)
	}
	// The counterweight: a true must be accepted, or the control could never arm.
	if err := deleteLanded(del, raw(`true`), "t"); err != nil {
		t.Errorf("a successful delete was rejected: %v", err)
	}
	// An answer that is not a boolean at all is inconclusive, never a verdict.
	assertCode(t, deleteLanded(del, raw(`{"deleted":true}`), "t"), exitSetup)
}

// A modify has no boolean to read, and must not be judged as if it did — the
// update returns an object, which would parse as neither true nor false.
func TestAModifyIsNotJudgedByADeletesAnswer(t *testing.T) {
	if err := deleteLanded(mustTamper(t, tamperModify), raw(`{"token":"t"}`), "t"); err != nil {
		t.Errorf("a modify's answer was read as a delete's: %v", err)
	}
}

func TestRecordedNamedFindsOnlyWhatTheReceiptCarries(t *testing.T) {
	rec := Receipt{Entities: []Recorded{{Name: "asset-type", Token: "a"}, {Name: "asset", Token: "b"}}}
	if r, ok := recordedNamed(rec, "asset"); !ok || r.Token != "b" {
		t.Errorf("recordedNamed(asset) = %+v, %v", r, ok)
	}
	if _, ok := recordedNamed(rec, "dashboard"); ok {
		t.Error("recordedNamed found an entity the receipt does not carry")
	}
}
