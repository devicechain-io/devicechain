// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/devicechain-io/dc-microservice/userclient"
)

// ---- Calculated metrics and device constants, through ensureProfile / ensureDevice --------------

func calculatedProfileSpec(t *testing.T) ProfileSpec {
	t.Helper()
	spec := widgetlabProfileSpec(t)
	spec.Metrics = append(spec.Metrics, MetricSpec{
		Key: "temp_f", Name: "Temperature F", DataType: "DOUBLE", Unit: "F",
		Expression: WidgetlabTemperatureKey + " * 1.8 + 32.0",
	})
	return spec
}

// A calculated metric is created WITH its formula, and a re-run that finds it unchanged does
// nothing: no update, no republish.
func TestEnsureProfileCreatesACalculatedMetricWithItsFormula(t *testing.T) {
	f, rt := newFakeProfileServer(t)
	spec := calculatedProfileSpec(t)
	if err := run(t, rt, spec); err != nil {
		t.Fatalf("ensureProfile: %v", err)
	}
	f.mu.Lock()
	got := f.expressions[fakeProfileToken+"-temp_f"]
	f.mu.Unlock()
	if got != "temperature * 1.8 + 32.0" {
		t.Errorf("the formula the server holds is %q", got)
	}
	versions, updates := f.versionCount(), f.updateCount()
	if err := run(t, rt, spec); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if f.versionCount() != versions || f.updateCount() != updates {
		t.Errorf("an unchanged calculated metric was rewritten: versions %d->%d updates %d->%d",
			versions, f.versionCount(), updates, f.updateCount())
	}
}

// A changed formula is converged and republished: otherwise a corrected formula could never reach
// a tenant that had already run the old build.
func TestEnsureProfileConvergesAChangedFormulaAndRepublishes(t *testing.T) {
	f, rt := newFakeProfileServer(t)
	spec := calculatedProfileSpec(t)
	if err := run(t, rt, spec); err != nil {
		t.Fatal(err)
	}
	versions := f.versionCount()
	spec.Metrics[len(spec.Metrics)-1].Expression = WidgetlabTemperatureKey + " * 1.8 + 32.0 - 1.0"
	if err := run(t, rt, spec); err != nil {
		t.Fatalf("re-run with a corrected formula: %v", err)
	}
	f.mu.Lock()
	got := f.expressions[fakeProfileToken+"-temp_f"]
	f.mu.Unlock()
	if !strings.HasSuffix(got, "- 1.0") {
		t.Errorf("the formula was not converged: %q", got)
	}
	if f.versionCount() != versions+1 {
		t.Errorf("the corrected formula was not republished (versions %d -> %d)", versions, f.versionCount())
	}
}

// A profile with no calculated metric keeps the content marker it had before formulas existed, so
// this build does not republish every existing scenario's profile for nothing.
func TestAFormulaOnlyChangesTheMarkerOfAProfileThatHasOne(t *testing.T) {
	plain := widgetlabProfileSpec(t)
	before := profileContentMarker(plain)
	with := calculatedProfileSpec(t)
	if profileContentMarker(with) == before {
		t.Error("adding a calculated metric did not change the marker, so it would never be published")
	}
	altered := calculatedProfileSpec(t)
	altered.Metrics[len(altered.Metrics)-1].Expression += " + 1.0"
	if profileContentMarker(altered) == profileContentMarker(with) {
		t.Error("changing a formula did not change the marker, so the change would never be republished")
	}
	// Pinned: the marker of a profile with no formula is what it was before this field existed.
	// (The widgetlab marker is asserted by TestEnsureProfileLabelIsReadableAndMarkerIsStored's
	// neighbours against the same function; here it is only required to be formula-free.)
	if strings.Contains(before, "formula") {
		t.Errorf("marker %q mentions a formula", before)
	}
}

// A metric whose kind disagrees with the draft cannot be converged (the kind is fixed when it is
// created), and says so plainly, in either direction.
func TestEnsureProfileRefusesToChangeAMetricsKind(t *testing.T) {
	f, rt := newFakeProfileServer(t)
	measured := widgetlabProfileSpec(t)
	measured.Metrics = append(measured.Metrics, MetricSpec{Key: "temp_f", Name: "Temperature F", DataType: "DOUBLE", Unit: "F"})
	if err := run(t, rt, measured); err != nil {
		t.Fatal(err)
	}
	err := run(t, rt, calculatedProfileSpec(t))
	if err == nil || !strings.Contains(err.Error(), "measured") || !strings.Contains(err.Error(), "calculated") {
		t.Fatalf("a measured metric redeclared as calculated: err = %v", err)
	}

	f2, rt2 := newFakeProfileServer(t)
	if err := run(t, rt2, calculatedProfileSpec(t)); err != nil {
		t.Fatal(err)
	}
	_ = f2
	err = run(t, rt2, measured)
	if err == nil || !strings.Contains(err.Error(), "calculated") {
		t.Fatalf("a calculated metric redeclared as measured: err = %v", err)
	}
	_ = f
}

// The tier ceiling for calculated metrics is 0 by default, and the platform refuses the publish.
// The refusal must say what to change, because the platform's own message names neither the tier
// key nor the way a sim tenant is packaged.
func TestAPublishRefusedForCalculatedMetricsNamesTheTierCeiling(t *testing.T) {
	f, rt := newFakeProfileServer(t)
	f.failNextPublish = true
	err := run(t, rt, calculatedProfileSpec(t))
	if err == nil {
		t.Fatal("a refused publish was reported as success")
	}
	for _, want := range []string{"calculatedMetricsPerProfile", "dcctl sim create --tier", "at least 1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	// A profile with no calculated metric gets the plain error: the hint would be wrong there.
	f2, rt2 := newFakeProfileServer(t)
	f2.failNextPublish = true
	err = run(t, rt2, widgetlabProfileSpec(t))
	if err == nil || strings.Contains(err.Error(), "calculatedMetricsPerProfile") {
		t.Errorf("a refusal on a profile with no formula blames the calculated-metrics ceiling: %v", err)
	}
}

// ---- A generic recording GraphQL fake, for the steps that have no stateful fake -----------------

type gqlCall struct {
	query string
	vars  map[string]any
}

type recordingGraphQL struct {
	mu    sync.Mutex
	calls []gqlCall
	// answer returns the data for an operation, or nil for "not modelled" (a 501, which fails the
	// test loudly rather than reading as an empty result).
	answer func(query string, vars map[string]any) map[string]any
}

func newRecordingGraphQL(t *testing.T, answer func(string, map[string]any) map[string]any) (*recordingGraphQL, *Runtime) {
	t.Helper()
	g := &recordingGraphQL{answer: answer}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var data map[string]any
		switch {
		case strings.Contains(req.Query, "login("):
			data = map[string]any{"login": map[string]any{"identityToken": "identity", "superuser": true}}
		case strings.Contains(req.Query, "selectTenant("):
			data = map[string]any{"selectTenant": map[string]any{"accessToken": "access", "refreshToken": "refresh"}}
		case strings.Contains(req.Query, "refresh("):
			// The session renews its token between calls; that is the session's business, not a
			// call the code under test made.
			data = map[string]any{"refresh": map[string]any{"accessToken": "access2", "refreshToken": "refresh2"}}
		default:
			g.mu.Lock()
			g.calls = append(g.calls, gqlCall{req.Query, req.Variables})
			data = g.answer(req.Query, req.Variables)
			g.mu.Unlock()
		}
		if data == nil {
			http.Error(w, "fake has no handler for: "+req.Query, http.StatusNotImplemented)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	return g, &Runtime{
		Endpoints:   Endpoints{DeviceMgmtGraphQL: srv.URL, UserGraphQL: srv.URL, DashboardMgmtGraphQL: srv.URL},
		InstanceId:  "dc",
		Tenant:      "acme",
		SimPassword: "sim-password",
		HTTPClient:  srv.Client(),
		Session:     userclient.NewTenantSession(srv.Client(), srv.URL, "sim@example.invalid", "pw", "acme"),
	}
}

func (g *recordingGraphQL) called(substr string) []gqlCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []gqlCall
	for _, c := range g.calls {
		if strings.Contains(c.query, substr) {
			out = append(out, c)
		}
	}
	return out
}

func (g *recordingGraphQL) names() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []string
	for _, c := range g.calls {
		switch {
		case strings.Contains(c.query, "inviteMember"):
			out = append(out, "inviteMember")
		case strings.Contains(c.query, "acceptInvitation"):
			out = append(out, "acceptInvitation")
		case strings.Contains(c.query, "shareDashboard"):
			out = append(out, "shareDashboard")
		case strings.Contains(c.query, "reissueInvitation"):
			out = append(out, "reissueInvitation")
		case strings.Contains(c.query, "setMemberAccess"):
			out = append(out, "setMemberAccess")
		case strings.Contains(c.query, "setMemberEnabled"):
			out = append(out, "setMemberEnabled")
		case strings.Contains(c.query, "setMemberScope"):
			out = append(out, "setMemberScope")
		case strings.Contains(c.query, "members("):
			out = append(out, "members")
		case strings.Contains(c.query, "updateDevice"):
			out = append(out, "updateDevice")
		case strings.Contains(c.query, "createDevice("):
			out = append(out, "createDevice")
		default:
			out = append(out, c.query)
		}
	}
	return out
}

// ---- Devices carry their constants --------------------------------------------------------------

func meterInstance(t *testing.T) DeviceInstance {
	t.Helper()
	m := NewTowerpulse(1, Load{}).Manifest()
	for _, d := range m.Expand(m.Seed) {
		if d.Token == "tp-meter-tower" {
			return d
		}
	}
	t.Fatal("no tower meter")
	return DeviceInstance{}
}

func deviceFake(existing map[string]*string) func(string, map[string]any) map[string]any {
	return func(q string, vars map[string]any) map[string]any {
		switch {
		case strings.Contains(q, "devicesByToken"):
			toks, _ := vars["tokens"].([]any)
			out := []any{}
			for _, tk := range toks {
				if md, ok := existing[tk.(string)]; ok {
					out = append(out, map[string]any{"token": tk, "metadata": md})
				}
			}
			return map[string]any{"devicesByToken": out}
		case strings.Contains(q, "createDevice("):
			return map[string]any{"createDevice": map[string]any{"token": "x"}}
		case strings.Contains(q, "updateDevice("):
			return map[string]any{"updateDevice": map[string]any{"token": "x"}}
		case strings.Contains(q, "deviceCredentialsByToken"):
			return map[string]any{"deviceCredentialsByToken": []any{map[string]any{"token": "c"}}}
		}
		return nil
	}
}

// The constants a calculated metric reads are written as the device's metadata when it is created.
func TestADeviceIsCreatedWithItsConstantsAsMetadata(t *testing.T) {
	g, rt := newRecordingGraphQL(t, deviceFake(map[string]*string{}))
	d := meterInstance(t)
	if err := ensureDevice(context.Background(), rt, d); err != nil {
		t.Fatal(err)
	}
	creates := g.called("createDevice(")
	if len(creates) != 1 {
		t.Fatalf("%d creates, want 1", len(creates))
	}
	req := creates[0].vars["request"].(map[string]any)
	if req["metadata"] != `{"floor_area_m2":6200}` {
		t.Errorf("metadata = %v, want the floor area", req["metadata"])
	}
}

// A device that exists with other constants (an earlier run with different values) has them
// converged; one that already agrees, in any key order or spacing, is left alone; and a device
// that carries no constants is never touched.
func TestADevicesConstantsAreConvergedAndAnAgreeingOneIsLeftAlone(t *testing.T) {
	d := meterInstance(t)

	stale := `{"floor_area_m2": 1}`
	g, rt := newRecordingGraphQL(t, deviceFake(map[string]*string{d.Token: &stale}))
	if err := ensureDevice(context.Background(), rt, d); err != nil {
		t.Fatal(err)
	}
	updates := g.called("updateDevice(")
	if len(updates) != 1 {
		t.Fatalf("%d updates for a stale constant, want 1", len(updates))
	}
	if updates[0].vars["token"] != d.Token || updates[0].vars["request"].(map[string]any)["metadata"] != d.Metadata {
		t.Errorf("update = %v", updates[0].vars)
	}
	if _, has := updates[0].vars["request"].(map[string]any)["token"]; has {
		t.Error("the update carried a token in its payload, which the dedicated update input does not have")
	}

	agreeing := ` { "floor_area_m2" : 6200.0 } `
	g2, rt2 := newRecordingGraphQL(t, deviceFake(map[string]*string{d.Token: &agreeing}))
	if err := ensureDevice(context.Background(), rt2, d); err != nil {
		t.Fatal(err)
	}
	if n := len(g2.called("updateDevice(")); n != 0 {
		t.Errorf("a device that already agrees was updated %d times", n)
	}

	plain := d
	plain.Metadata = ""
	other := `{"anything":1}`
	g3, rt3 := newRecordingGraphQL(t, deviceFake(map[string]*string{plain.Token: &other}))
	if err := ensureDevice(context.Background(), rt3, plain); err != nil {
		t.Fatal(err)
	}
	if n := len(g3.called("updateDevice(")); n != 0 {
		t.Errorf("a device with no constants was rewritten (%d updates)", n)
	}
}

// ---- The customer user --------------------------------------------------------------------------

func membersFake(existing []map[string]any, invitationCode string) func(string, map[string]any) map[string]any {
	return func(q string, vars map[string]any) map[string]any {
		switch {
		case strings.Contains(q, "members("):
			return map[string]any{"members": map[string]any{"results": existing}}
		case strings.Contains(q, "inviteMember("):
			return map[string]any{"inviteMember": map[string]any{"invitationCode": invitationCode, "member": map[string]any{"email": "x"}}}
		case strings.Contains(q, "acceptInvitation("):
			return map[string]any{"acceptInvitation": map[string]any{"identityToken": "t"}}
		case strings.Contains(q, "setMemberScope("):
			return map[string]any{"setMemberScope": map[string]any{"email": "x"}}
		case strings.Contains(q, "setMemberAccess("):
			return map[string]any{"setMemberAccess": map[string]any{"email": "x"}}
		case strings.Contains(q, "setMemberEnabled("):
			return map[string]any{"setMemberEnabled": map[string]any{"email": "x"}}
		case strings.Contains(q, "reissueInvitation("):
			return map[string]any{"reissueInvitation": map[string]any{"invitationCode": "reissued-1"}}
		}
		return nil
	}
}

var viewerRoles = []map[string]any{{"token": "viewer"}}

func annexOwner() CustomerUserSpec {
	return CustomerUserSpec{Email: TowerpulseAnnexOwnerEmail, CustomerToken: TowerpulseAnnexOwnerToken}
}

// The annex owner is invited with the read-only role and a scope of exactly ONE customer anchor,
// the annex owner's, and accepts at once with the sim's password so the account is usable.
func TestTheAnnexOwnerIsInvitedScopedToTheAnnexAndAcceptsAtOnce(t *testing.T) {
	g, rt := newRecordingGraphQL(t, membersFake(nil, "code-123"))
	if err := ensureCustomerUser(context.Background(), rt, annexOwner()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(g.names(), ","); got != "members,inviteMember,acceptInvitation" {
		t.Fatalf("calls = %s, want members, inviteMember, acceptInvitation in that order", got)
	}
	invite := g.called("inviteMember(")[0].vars["request"].(map[string]any)
	if invite["email"] != TowerpulseAnnexOwnerEmail {
		t.Errorf("invited %v", invite["email"])
	}
	if roles := invite["roleTokens"].([]any); len(roles) != 1 || roles[0] != "viewer" {
		t.Errorf("roles = %v, want the read-only viewer role alone", roles)
	}
	scope := invite["scope"].([]any)
	if len(scope) != 1 {
		t.Fatalf("scope = %v: an empty scope is the WHOLE tenant, and more than one is not the annex alone", scope)
	}
	if a := scope[0].(map[string]any); a["kind"] != "CUSTOMER" || a["token"] != TowerpulseAnnexOwnerToken {
		t.Errorf("scope anchor = %v, want CUSTOMER %s", a, TowerpulseAnnexOwnerToken)
	}
	accept := g.called("acceptInvitation(")[0].vars
	if accept["code"] != "code-123" || accept["password"] != "sim-password" {
		t.Errorf("accept = %v, want the invitation code and the sim password", accept)
	}
}

// The scenario's own manifest declares exactly this user, on the annex owner.
func TestTheManifestProvisionsTheAnnexOwnerAsItsOnlyCustomerUser(t *testing.T) {
	m := towerpulseManifest(t)
	if len(m.CustomerUsers) != 1 || m.CustomerUsers[0] != annexOwner() {
		t.Fatalf("customer users = %+v", m.CustomerUsers)
	}
	bad := m
	bad.CustomerUsers = []CustomerUserSpec{{Email: "x@example.test", CustomerToken: "no-such-customer"}}
	if err := bad.Validate(); err == nil {
		t.Error("a customer user limited to a customer that does not exist validated")
	}
	bad.CustomerUsers = []CustomerUserSpec{{Email: "not-an-email", CustomerToken: TowerpulseAnnexOwnerToken}}
	if err := bad.Validate(); err == nil {
		t.Error("a customer user with no email address validated")
	}
}

// Idempotent: a member who already has exactly the right scope is not invited, accepted or touched.
func TestACustomerUserWhoAlreadyHasTheRightScopeIsLeftAlone(t *testing.T) {
	existing := []map[string]any{{
		"email": TowerpulseAnnexOwnerEmail, "enabled": true, "invitationPending": false, "roles": viewerRoles,
		"scope": []map[string]any{{"kind": "CUSTOMER", "token": TowerpulseAnnexOwnerToken}},
	}}
	g, rt := newRecordingGraphQL(t, membersFake(existing, ""))
	if err := ensureCustomerUser(context.Background(), rt, annexOwner()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(g.names(), ","); got != "members" {
		t.Errorf("calls = %s, want only the lookup", got)
	}
}

// A member someone WIDENED (an empty scope is the whole tenant) or pointed at the tower owner is
// narrowed back to the annex, and not invited a second time.
func TestACustomerUserWhoWasWidenedOrMovedIsNarrowedBackToTheAnnex(t *testing.T) {
	for name, scope := range map[string][]map[string]any{
		"whole tenant":  {},
		"the tower":     {{"kind": "CUSTOMER", "token": TowerpulseTowerOwnerToken}},
		"both":          {{"kind": "CUSTOMER", "token": TowerpulseTowerOwnerToken}, {"kind": "CUSTOMER", "token": TowerpulseAnnexOwnerToken}},
		"a group scope": {{"kind": "GROUP", "token": TowerpulseAnnexOwnerToken}},
	} {
		t.Run(name, func(t *testing.T) {
			existing := []map[string]any{{"email": strings.ToUpper(TowerpulseAnnexOwnerEmail), "enabled": true, "roles": viewerRoles, "scope": scope}}
			g, rt := newRecordingGraphQL(t, membersFake(existing, ""))
			if err := ensureCustomerUser(context.Background(), rt, annexOwner()); err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(g.names(), ","); got != "members,setMemberScope" {
				t.Fatalf("calls = %s, want members then setMemberScope", got)
			}
			sent := g.called("setMemberScope(")[0].vars["scope"].([]any)
			if len(sent) != 1 || sent[0].(map[string]any)["token"] != TowerpulseAnnexOwnerToken || sent[0].(map[string]any)["kind"] != "CUSTOMER" {
				t.Errorf("scope sent = %v, want the annex owner alone", sent)
			}
		})
	}
}

var annexScope = []map[string]any{{"kind": "CUSTOMER", "token": TowerpulseAnnexOwnerToken}}

// An earlier run invited the person and failed before accepting: they exist, with the right scope,
// and can never sign in. The bootstrap issues a new code and accepts it with the sim password.
func TestAPendingCustomerUserInvitationIsReissuedAndAccepted(t *testing.T) {
	existing := []map[string]any{{
		"email": TowerpulseAnnexOwnerEmail, "enabled": false, "invitationPending": true,
		"roles": viewerRoles, "scope": annexScope,
	}}
	g, rt := newRecordingGraphQL(t, membersFake(existing, ""))
	if err := ensureCustomerUser(context.Background(), rt, annexOwner()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(g.names(), ","); got != "members,reissueInvitation,acceptInvitation" {
		t.Fatalf("calls = %s, want members, reissueInvitation, acceptInvitation", got)
	}
	if a := g.called("acceptInvitation(")[0].vars; a["code"] != "reissued-1" || a["password"] != "sim-password" {
		t.Errorf("accept = %v, want the REISSUED code and the sim password", a)
	}
}

// Negative control: a pending member with a refused reissue is an error naming the member, never a
// quiet success that leaves an account nobody can sign in to.
func TestAPendingCustomerUserWhoCannotBeReissuedIsAnErrorNamingThem(t *testing.T) {
	existing := []map[string]any{{
		"email": TowerpulseAnnexOwnerEmail, "invitationPending": true, "roles": viewerRoles, "scope": annexScope,
	}}
	fake := membersFake(existing, "")
	_, rt := newRecordingGraphQL(t, func(q string, v map[string]any) map[string]any {
		if strings.Contains(q, "reissueInvitation(") {
			return map[string]any{"reissueInvitation": map[string]any{"invitationCode": ""}}
		}
		return fake(q, v)
	})
	err := ensureCustomerUser(context.Background(), rt, annexOwner())
	if err == nil || !strings.Contains(err.Error(), TowerpulseAnnexOwnerEmail) {
		t.Fatalf("err = %v, want an error naming the member", err)
	}
}

// A member holding a broader role is put back to the viewer role in the same step as the scope, and
// the scope sent is still the annex alone. A disabled (not pending) member is re-enabled.
func TestACustomerUserWithTheWrongRoleOrDisabledIsConverged(t *testing.T) {
	wrong := []map[string]any{{
		"email": TowerpulseAnnexOwnerEmail, "enabled": true, "roles": []map[string]any{{"token": "admin"}}, "scope": annexScope,
	}}
	g, rt := newRecordingGraphQL(t, membersFake(wrong, ""))
	if err := ensureCustomerUser(context.Background(), rt, annexOwner()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(g.names(), ","); got != "members,setMemberAccess" {
		t.Fatalf("calls = %s", got)
	}
	v := g.called("setMemberAccess(")[0].vars
	if r := v["roleTokens"].([]any); len(r) != 1 || r[0] != "viewer" {
		t.Errorf("roles = %v", r)
	}
	if sc := v["scope"].([]any); len(sc) != 1 || sc[0].(map[string]any)["token"] != TowerpulseAnnexOwnerToken {
		t.Errorf("scope = %v, must stay the annex alone", sc)
	}

	off := []map[string]any{{
		"email": TowerpulseAnnexOwnerEmail, "enabled": false, "invitationPending": false, "roles": viewerRoles, "scope": annexScope,
	}}
	g2, rt2 := newRecordingGraphQL(t, membersFake(off, ""))
	if err := ensureCustomerUser(context.Background(), rt2, annexOwner()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(g2.names(), ","); got != "members,setMemberEnabled" {
		t.Fatalf("calls = %s", got)
	}
}

// Fail-closed: a customer user is never created without a customer, or without a way to accept.
func TestACustomerUserIsNeverCreatedWithoutAScopeOrAPassword(t *testing.T) {
	g, rt := newRecordingGraphQL(t, membersFake(nil, "c"))
	if err := ensureCustomerUser(context.Background(), rt, CustomerUserSpec{Email: "a@b.test"}); err == nil {
		t.Error("a customer user with no customer was created: an empty scope is the whole tenant")
	}
	rt.SimPassword = ""
	if err := ensureCustomerUser(context.Background(), rt, annexOwner()); err == nil {
		t.Error("a customer user was created with nothing to accept the invitation with")
	}
	if n := len(g.calls); n != 0 {
		t.Errorf("%d calls were made before the refusal", n)
	}
}

// What the annex owner can see is what is assigned to the annex owner's customer: the seven annex
// devices, and none of the tower's. The scope is a customer anchor, so this is the whole
// definition of "only the annex".
func TestTheAnnexOwnersScopeCoversExactlyTheAnnexDevices(t *testing.T) {
	m := towerpulseManifest(t)
	var inScope, outOfScope []string
	for _, d := range m.Expand(m.Seed) {
		owned := false
		for _, a := range d.Assignments {
			if a.TargetType == "customer" && a.TargetToken == m.CustomerUsers[0].CustomerToken {
				owned = true
			}
		}
		if owned {
			inScope = append(inScope, d.Token)
		} else {
			outOfScope = append(outOfScope, d.Token)
		}
	}
	if len(inScope) != 7 || len(outOfScope) != 24 {
		t.Fatalf("%d devices in the annex owner's scope and %d outside it, want 7 and 24", len(inScope), len(outOfScope))
	}
	for _, tok := range inScope {
		if !strings.Contains(tok, "annex") {
			t.Errorf("%s is in the annex owner's scope but is not an annex device", tok)
		}
	}
	for _, tok := range outOfScope {
		if strings.Contains(tok, "annex") {
			t.Errorf("%s is an annex device the annex owner cannot see", tok)
		}
	}
}

// ---- Who can see the annex board ------------------------------------------------------------------

func dashboardsFake(token string) func(string, map[string]any) map[string]any {
	return func(q string, vars map[string]any) map[string]any {
		switch {
		case strings.Contains(q, "shareDashboard("):
			return map[string]any{"shareDashboard": map[string]any{"token": token}}
		case strings.Contains(q, "updateDashboard("):
			return map[string]any{"updateDashboard": map[string]any{"token": token}}
		case strings.Contains(q, "dashboardVersions("):
			return map[string]any{"dashboardVersions": []map[string]any{{"version": 1}}}
		case strings.Contains(q, "dashboard(token"):
			return map[string]any{"dashboard": map[string]any{"token": token}}
		}
		return nil
	}
}

func dashboardSpec(t *testing.T, token string) DashboardSpec {
	t.Helper()
	for _, ds := range towerpulseManifest(t).Dashboards {
		if ds.Token == token {
			return ds
		}
	}
	t.Fatalf("no dashboard %s", token)
	return DashboardSpec{}
}

// A member limited to a customer lists only the dashboards shared with that customer, so the annex
// board is shared with the annex owner and with no one else; the operator's board is not shared at all.
func TestTheAnnexBoardIsSharedWithTheAnnexOwnerAlone(t *testing.T) {
	g, rt := newRecordingGraphQL(t, dashboardsFake(TowerpulseAnnexDashboardToken))
	if err := ensureDashboard(context.Background(), rt, dashboardSpec(t, TowerpulseAnnexDashboardToken)); err != nil {
		t.Fatal(err)
	}
	shares := g.called("shareDashboard(")
	if len(shares) != 1 {
		t.Fatalf("%d shares, want 1", len(shares))
	}
	anchors := shares[0].vars["anchors"].([]any)
	if len(anchors) != 1 || anchors[0].(map[string]any)["kind"] != "CUSTOMER" || anchors[0].(map[string]any)["token"] != TowerpulseAnnexOwnerToken {
		t.Errorf("anchors = %v, want the annex owner alone", anchors)
	}

	// Negative control: the operator's board carries no share, and sends none.
	g2, rt2 := newRecordingGraphQL(t, dashboardsFake(TowerpulseDashboardToken))
	if err := ensureDashboard(context.Background(), rt2, dashboardSpec(t, TowerpulseDashboardToken)); err != nil {
		t.Fatal(err)
	}
	if n := len(g2.called("shareDashboard(")); n != 0 {
		t.Errorf("the operator's board was shared %d times", n)
	}
}

func TestADashboardSharedWithAnUnknownCustomerDoesNotValidate(t *testing.T) {
	m := towerpulseManifest(t)
	m.Dashboards = append([]DashboardSpec(nil), m.Dashboards...)
	m.Dashboards[1].SharedWithCustomers = []string{"no-such-customer"}
	if err := m.Validate(); err == nil {
		t.Error("a dashboard shared with a customer that does not exist validated")
	}
}
