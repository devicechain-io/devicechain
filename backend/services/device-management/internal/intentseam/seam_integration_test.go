// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package intentseam_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/internal/intentseam"
	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/schema"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/intent"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"gorm.io/gorm"
)

// The seam against a real PostgreSQL, through the real device-management migrations and, for
// the small fixtures, the real write paths (profiles published, groups published, attributes
// written through SetEntityAttribute), so the snapshot and attribute rows the SQL reads are the
// ones production writes.

func itEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

var (
	mgrOnce sync.Once
	mgr     *rdb.RdbManager
	mgrErr  error
)

// seamManager migrates one instance database for the whole package and shares it: every test
// works in a tenant of its own, so they cannot see each other.
func seamManager(t testing.TB) *rdb.RdbManager {
	t.Helper()
	mgrOnce.Do(func() {
		port, err := strconv.Atoi(itEnv("DC_IT_PGPORT", "5432"))
		if err != nil {
			mgrErr = err
			return
		}
		host, user, pass := itEnv("DC_IT_PGHOST", "localhost"), itEnv("DC_IT_PGUSER", "postgres"), itEnv("DC_IT_PGPASSWORD", "postgres")
		if mgrErr = rdbtest.EnsureDatabase(context.Background(), host, port, user, pass, "dcintentseam", ""); mgrErr != nil {
			return
		}
		mgr = &rdb.RdbManager{
			Microservice: &core.Microservice{InstanceId: "dcintentseam", FunctionalArea: "device-management"},
			Migrations:   schema.Migrations,
			InstanceConfig: config.DatastoreConfiguration{
				Type: "postgres95",
				Configuration: map[string]interface{}{
					"hostname": host, "port": port, "username": user, "password": pass,
				},
			},
		}
		mgrErr = mgr.ExecuteInitialize(context.Background())
	})
	if mgrErr != nil {
		t.Fatalf("device-management on a real PostgreSQL: %v", mgrErr)
	}
	return mgr
}

func newSeamApi(t testing.TB) *model.Api {
	return model.NewApi(seamManager(t))
}

// fleet builds one tenant's fixture through the real write paths.
type fleet struct {
	t      *testing.T
	api    *model.Api
	ctx    context.Context
	tenant string
}

func newFleet(t *testing.T, api *model.Api, name string) *fleet {
	tenant := fmt.Sprintf("%s-%d", name, time.Now().UnixNano())
	return &fleet{t: t, api: api, ctx: core.WithTenant(context.Background(), tenant), tenant: tenant}
}

func (f *fleet) must(err error, what string) {
	f.t.Helper()
	if err != nil {
		f.t.Fatalf("%s: %v", what, err)
	}
}

// profile creates, declares and publishes a profile. With no keys it is created and left
// unpublished.
func (f *fleet) profile(token string, publish bool, keys ...model.ConfigurationKey) {
	f.t.Helper()
	_, err := f.api.CreateDeviceProfile(f.ctx, &model.DeviceProfileCreateRequest{Token: token})
	f.must(err, "create profile "+token)
	if len(keys) > 0 {
		_, err = f.api.SetDeviceProfileConfigurationDeclaration(f.ctx, token, keys)
		f.must(err, "declare "+token)
	}
	if publish {
		_, err = f.api.PublishDeviceProfile(f.ctx, token, nil, nil, "tester")
		f.must(err, "publish "+token)
	}
}

func (f *fleet) republish(token string, keys ...model.ConfigurationKey) {
	f.t.Helper()
	_, err := f.api.SetDeviceProfileConfigurationDeclaration(f.ctx, token, keys)
	f.must(err, "declare "+token)
	_, err = f.api.PublishDeviceProfile(f.ctx, token, nil, nil, "tester")
	f.must(err, "publish "+token)
}

func (f *fleet) deviceType(token, profile string) {
	f.t.Helper()
	req := &model.DeviceTypeCreateRequest{Token: token}
	if profile != "" {
		req.ProfileToken = &profile
	}
	_, err := f.api.CreateDeviceType(f.ctx, req)
	f.must(err, "create type "+token)
}

func (f *fleet) device(token, typeToken string, attrs ...string) *model.Device {
	f.t.Helper()
	d, err := f.api.CreateDevice(f.ctx, &model.DeviceCreateRequest{Token: token, DeviceTypeToken: typeToken})
	f.must(err, "create device "+token)
	for i := 0; i+2 < len(attrs); i += 3 {
		f.attr(token, attrs[i], attrs[i+1], attrs[i+2])
	}
	return d
}

func (f *fleet) attr(device, key, valueType, value string) {
	f.t.Helper()
	_, err := f.api.SetEntityAttribute(f.ctx, &model.EntityAttributeSetRequest{
		EntityType: "device", Entity: device, Scope: "SHARED", AttrKey: key, ValueType: valueType, Value: &value})
	f.must(err, fmt.Sprintf("set %s on %s", key, device))
}

// group creates and publishes a dynamic device group, returning the published version.
func (f *fleet) group(token, selector string) int32 {
	f.t.Helper()
	dynamic := "dynamic"
	_, err := f.api.CreateEntityGroup(f.ctx, &model.EntityGroupCreateRequest{
		Token: token, MemberType: "device", MembershipMode: &dynamic, Selector: &selector})
	f.must(err, "create group "+token)
	return f.publishGroup(token)
}

func (f *fleet) publishGroup(token string) int32 {
	f.t.Helper()
	v, err := f.api.PublishEntityGroup(f.ctx, token, nil, nil, "tester")
	f.must(err, "publish group "+token)
	return v.Version
}

// setDraftSelector edits a group's draft selector without publishing it.
func (f *fleet) setDraftSelector(token, selector string) {
	f.t.Helper()
	f.exec(`UPDATE entity_groups SET selector = ? WHERE tenant_id = ? AND token = ?`, selector, f.tenant, token)
}

func (f *fleet) exec(q string, args ...any) {
	f.t.Helper()
	err := f.api.RDB.Database.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT set_config('devicechain.attr_protocol', '1', true)").Error; err != nil {
			return err
		}
		return tx.Exec(q, args...).Error
	})
	f.must(err, q)
}

func (f *fleet) digest(group string, version int32, key string) *intentseam.DigestAnswer {
	f.t.Helper()
	out, err := f.api.ConfigurationKeyStateDigest(f.ctx, model.IntentSeamTarget{GroupToken: group, Version: version}, key)
	f.must(err, "digest "+group)
	return out
}

// walk pages the whole target and returns the rows.
func (f *fleet) walk(group string, version int32, key string, limit int) ([]intentseam.Row, *intentseam.PageAnswer) {
	f.t.Helper()
	var rows []intentseam.Row
	var after int64
	for i := 0; ; i++ {
		page, err := f.api.ConfigurationKeyState(f.ctx, model.IntentSeamTarget{GroupToken: group, Version: version}, key, after, limit)
		f.must(err, "page "+group)
		if page.Rejected {
			return rows, page
		}
		rows = append(rows, page.Rows...)
		if page.NextCursor == 0 {
			return rows, page
		}
		if page.NextCursor <= after {
			f.t.Fatalf("the cursor did not advance: %d after %d", page.NextCursor, after)
		}
		after = page.NextCursor
		if i > 1_000_000 {
			f.t.Fatal("the walk did not end")
		}
	}
}

// rehash hashes paged rows with core/intent's hasher, the one the automation area re-hashes with.
func rehash(t *testing.T, rows []intentseam.Row) string {
	t.Helper()
	h := intent.NewObservationHasher()
	for _, r := range rows {
		if err := h.Add(intent.Row{DeviceID: r.DeviceID, Token: r.Token, DeclaredValueType: r.DeclaredValueType,
			Present: r.Present, ValueType: r.ValueType, ValueDigest: r.ValueDigest}); err != nil {
			t.Fatalf("core/intent refuses a seam row: %v", err)
		}
	}
	return h.Sum()
}

func declared(rows []intentseam.Row) map[string]string {
	out := map[string]string{}
	for _, r := range rows {
		v := "<none>"
		if r.DeclaredValueType != nil {
			v = *r.DeclaredValueType
		}
		out[r.Token] = v
	}
	return out
}

func declarationCounts(d *intentseam.DigestAnswer) map[string]int64 {
	out := map[string]int64{}
	for _, c := range d.Declarations {
		k := "<none>"
		if c.DeclaredValueType != nil {
			k = *c.DeclaredValueType
		}
		out[k] = c.Count
	}
	return out
}

var (
	dimLong = model.ConfigurationKey{Key: "dim", ValueType: "LONG"}
	dimBool = model.ConfigurationKey{Key: "dim", ValueType: "BOOLEAN"}
	other   = model.ConfigurationKey{Key: "other", ValueType: "STRING"}
)

// A standard small fleet: every way a member can relate to the key's declaration.
//
//	lamp-1  type t-long     profile p-long (dim LONG)   dim = 40
//	lamp-2  type t-long     profile p-long              dim = 41
//	lamp-3  type t-long     profile p-long              no dim
//	lamp-4  type t-bool     profile p-bool (dim BOOLEAN) dim = true
//	lamp-5  type t-other    profile p-other (declares only "other")
//	lamp-6  type t-draft    profile p-draft, never published
//	lamp-7  type t-none     no profile
//	lamp-8  type t-long     zone east, not in the quay group
func standardFleet(t *testing.T, api *model.Api, name string) *fleet {
	f := newFleet(t, api, name)
	f.profile("p-long", true, dimLong)
	f.profile("p-bool", true, dimBool)
	f.profile("p-other", true, other)
	f.profile("p-draft", false, dimLong)
	f.deviceType("t-long", "p-long")
	f.deviceType("t-bool", "p-bool")
	f.deviceType("t-other", "p-other")
	f.deviceType("t-draft", "p-draft")
	f.deviceType("t-none", "")
	f.device("lamp-1", "t-long", "zone", "STRING", "quay", "dim", "LONG", "40")
	f.device("lamp-2", "t-long", "zone", "STRING", "quay", "dim", "LONG", "41")
	f.device("lamp-3", "t-long", "zone", "STRING", "quay")
	f.device("lamp-4", "t-bool", "zone", "STRING", "quay", "dim", "BOOLEAN", "true")
	f.device("lamp-5", "t-other", "zone", "STRING", "quay")
	f.device("lamp-6", "t-draft", "zone", "STRING", "quay")
	f.device("lamp-7", "t-none", "zone", "STRING", "quay")
	f.device("lamp-8", "t-long", "zone", "STRING", "east", "dim", "LONG", "40")
	return f
}

func TestDigestIsTheHashOfThePagedRows(t *testing.T) {
	api := newSeamApi(t)
	f := standardFleet(t, api, "seam-rows")
	v := f.group("quay", `attr["zone"] == "quay"`)
	d := f.digest("quay", v, "dim")
	if d.Rejected {
		t.Fatalf("the quay group was rejected: %+v", d.Rejection)
	}
	rows, _ := f.walk("quay", v, "dim", 3) // a page smaller than the target
	if d.MemberCount != 7 || len(rows) != 7 {
		t.Fatalf("memberCount %d, %d paged rows; want 7 and 7", d.MemberCount, len(rows))
	}
	if got := rehash(t, rows); got != d.Digest {
		t.Fatalf("the digest %s is not the hash of the paged rows %s", d.Digest, got)
	}
	want := map[string]string{"lamp-1": "LONG", "lamp-2": "LONG", "lamp-3": "LONG", "lamp-4": "BOOLEAN",
		"lamp-5": "<none>", "lamp-6": "<none>", "lamp-7": "<none>"}
	if got := declared(rows); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("declarations by device = %v, want %v", got, want)
	}
	if got, want := declarationCounts(d), map[string]int64{"LONG": 3, "BOOLEAN": 1, "<none>": 3}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("declaration counts = %v, want %v", got, want)
	}
	if d.PresentCount != 3 || d.OversizeCount != 0 {
		t.Errorf("present %d, oversize %d; want 3 and 0", d.PresentCount, d.OversizeCount)
	}
	// The value digest computed in SQL is core/intent's ValueDigest of the stored value.
	byToken := map[string]intentseam.Row{}
	for _, r := range rows {
		byToken[r.Token] = r
	}
	for token, vt := range map[string][2]string{"lamp-1": {"LONG", "40"}, "lamp-2": {"LONG", "41"}, "lamp-4": {"BOOLEAN", "true"}} {
		want, _ := intent.ValueDigest(vt[0], vt[1])
		if r := byToken[token]; !r.Present || r.ValueType != vt[0] || r.ValueDigest != want {
			t.Errorf("%s = %+v; want present %s %s", token, r, vt[0], want)
		}
	}
	if r := byToken["lamp-3"]; r.Present || r.ValueType != "" || r.ValueDigest != "" {
		t.Errorf("an absent value = %+v", r)
	}
	// And core/intent's diff over the rows classifies every relation.
	spec, err := intent.Parse([]byte(`{"target":{"groupToken":"quay","groupVersion":` + strconv.Itoa(int(v)) + `},"point":{"configKey":"dim"},` +
		`"value":{"valueType":"LONG","value":"40"},"class":"SCHEDULE","ceiling":{"maxDevices":100},` +
		`"reconcile":{"minIntervalSeconds":60,"maxLinesPerReconcile":100,"maxActuationsPerDevicePerDay":4}}`))
	f.must(err, "parse spec")
	var irows []intent.Row
	for _, r := range rows {
		irows = append(irows, intent.Row{DeviceID: r.DeviceID, Token: r.Token, DeclaredValueType: r.DeclaredValueType,
			Present: r.Present, ValueType: r.ValueType, ValueDigest: r.ValueDigest})
	}
	drift, err := intent.Diff(spec, irows, time.Now())
	f.must(err, "diff")
	if c := drift.Counts; c.InSync != 1 || c.Drifted != 2 || c.DeclaredTypeMismatch != 1 || c.KeyUndeclared != 3 {
		t.Errorf("diff counts = %+v; want 1 in sync, 2 drifted, 1 mismatch, 3 undeclared", c)
	}
}

func TestValueDigestInSQLIsCoreIntentsValueDigest(t *testing.T) {
	api := newSeamApi(t)
	f := newFleet(t, api, "seam-valuedigest")
	f.profile("p", true, model.ConfigurationKey{Key: "s", ValueType: "STRING"}, model.ConfigurationKey{Key: "j", ValueType: "JSON"})
	f.deviceType("t", "p")
	values := map[string][2]string{
		"d1": {"STRING", "plain"},
		"d2": {"STRING", "quotes \" back\\slash \t tab \n newline"},
		"d3": {"STRING", "ünïcødé ☃ 𝄞"},
		"d4": {"JSON", `{"a":[1,2,{"b":"é"}]}`},
		"d5": {"STRING", strings.Repeat("é", 512)}, // 1,024 bytes: hashed
		"d6": {"STRING", ""},
	}
	for token, v := range values {
		key := "s"
		if v[0] == "JSON" {
			key = "j"
		}
		f.device(token, "t", "zone", "STRING", "q", key, v[0], v[1])
	}
	gv := f.group("g", `attr["zone"] == "q"`)
	for _, key := range []string{"s", "j"} {
		rows, _ := f.walk("g", gv, key, 1000)
		for _, r := range rows {
			v := values[r.Token]
			if !r.Present {
				continue
			}
			want, err := intent.ValueDigest(v[0], v[1])
			f.must(err, "value digest")
			if r.ValueDigest != want {
				t.Errorf("%s %s: SQL digest %s, Go digest %s", r.Token, key, r.ValueDigest, want)
			}
		}
	}
}

func TestMembersWithoutADeclarationAreKeptAndCounted(t *testing.T) {
	api := newSeamApi(t)
	f := standardFleet(t, api, "seam-undeclared")
	// A target made only of members that declare nothing: no profile, an unpublished profile,
	// a profile that does not declare the key.
	v := f.group("undeclared", `attr["zone"] == "quay" && !("dim" in attr)`)
	d := f.digest("undeclared", v, "dim")
	// lamp-3 (declares, no value), lamp-5, lamp-6, lamp-7.
	if d.Rejected || d.MemberCount != 4 {
		t.Fatalf("digest = %+v; want 4 members kept", d)
	}
	if got := declarationCounts(d); got["<none>"] != 3 || got["LONG"] != 1 {
		t.Errorf("declarations = %v; want 3 undeclared and lamp-3's LONG", got)
	}
	v2 := f.group("only-undeclared", `"zone" in attr && !("dim" in attr) && !(attr["zone"] == "east")`)
	f.exec(`UPDATE devices SET device_type_id = (SELECT id FROM device_types WHERE tenant_id = ? AND token = 't-none') WHERE tenant_id = ? AND token = 'lamp-3'`, f.tenant, f.tenant)
	d2 := f.digest("only-undeclared", v2, "dim")
	if d2.Rejected || d2.MemberCount != 4 || declarationCounts(d2)["<none>"] != 4 {
		t.Fatalf("an undeclared-only target = %+v; want memberCount 4, all undeclared, never empty", d2)
	}
	rows, _ := f.walk("only-undeclared", v2, "dim", 1000)
	if len(rows) != 4 {
		t.Fatalf("pages hold %d rows; want 4", len(rows))
	}
	for _, r := range rows {
		if r.DeclaredValueType != nil {
			t.Errorf("%s reads declared %s", r.Token, *r.DeclaredValueType)
		}
	}
	// A soft-deleted device type or profile on the chain removes the declaration, never the
	// member.
	f.exec(`UPDATE device_types SET deleted_at = now() WHERE tenant_id = ? AND token = 't-long'`, f.tenant)
	d3 := f.digest("quay", f.group("quay", `attr["zone"] == "quay"`), "dim")
	if d3.Rejected || d3.MemberCount != 7 {
		t.Fatalf("with a deleted device type the target = %+v; want all 7 members kept", d3)
	}
}

func TestABrokenActivePointerIsRefusedNotUndeclared(t *testing.T) {
	api := newSeamApi(t)
	f := standardFleet(t, api, "seam-broken")
	v := f.group("quay", `attr["zone"] == "quay"`)
	before := f.digest("quay", v, "dim")
	if before.Rejected {
		t.Fatalf("before the breach: %+v", before.Rejection)
	}
	// The profile's active version is soft-deleted: its pointer now names no live version.
	f.exec(`UPDATE device_profile_versions SET deleted_at = now() WHERE tenant_id = ? AND device_profile_id =
		(SELECT id FROM device_profiles WHERE tenant_id = ? AND token = 'p-bool')`, f.tenant, f.tenant)
	d := f.digest("quay", v, "dim")
	if !d.Rejected || d.Code != intentseam.CodeProfileVersionMissing || d.Digest != "" {
		t.Fatalf("a broken pointer = %+v; want PROFILE_VERSION_MISSING with no digest", d)
	}
	rows, page := f.walk("quay", v, "dim", 1000)
	if !page.Rejected || page.Code != intentseam.CodeProfileVersionMissing || len(rows) != 0 {
		t.Fatalf("a page over a broken pointer = %+v (%d rows); want PROFILE_VERSION_MISSING", page.Rejection, len(rows))
	}
}

func TestANegatedSelectorNeverReachesAnotherTenant(t *testing.T) {
	api := newSeamApi(t)
	a := standardFleet(t, api, "seam-iso-a")
	b := standardFleet(t, api, "seam-iso-b")
	b.device("b-only", "t-long", "zone", "STRING", "harbour", "dim", "LONG", "40")
	va := a.group("not-quay", `!(attr["zone"] == "quay")`)
	vb := b.group("harbour", `attr["zone"] == "harbour"`)
	d := a.digest("not-quay", va, "dim")
	if d.Rejected || d.MemberCount != 1 {
		t.Fatalf("tenant A's negated group = %+v; want its one east device and no device of B", d)
	}
	rows, _ := a.walk("not-quay", va, "dim", 1000)
	if len(rows) != 1 || rows[0].Token != "lamp-8" {
		t.Fatalf("tenant A's pages = %+v; want only lamp-8", rows)
	}
	// A's negated group and A's own harbour selector share nothing; B's harbour devices are
	// never in A's answer.
	a.group("harbour", `attr["zone"] == "harbour"`)
	got, err := api.GroupTargetsIntersect(a.ctx, model.IntentSeamTarget{GroupToken: "harbour", Version: 1},
		model.IntentSeamTarget{GroupToken: "not-quay", Version: va})
	a.must(err, "intersect")
	if got.Rejected || got.Intersects {
		t.Fatalf("A's harbour ∩ A's not-quay = %+v; B's harbour device must not count", got)
	}
	// Another tenant's group is the same rejection as no group at all.
	if own := b.digest("harbour", vb, "dim"); own.Rejected || own.MemberCount != 1 {
		t.Fatalf("B's own group = %+v", own)
	}
	cross, err := api.ConfigurationKeyStateDigest(a.ctx, model.IntentSeamTarget{GroupToken: "only-in-b", Version: 1}, "dim")
	a.must(err, "digest")
	b.group("only-in-b", `"zone" in attr`)
	crossAfter, err := api.ConfigurationKeyStateDigest(a.ctx, model.IntentSeamTarget{GroupToken: "only-in-b", Version: 1}, "dim")
	a.must(err, "digest")
	if !cross.Rejected || cross.Code != intentseam.CodeGroupNotFound || crossAfter.Rejection != cross.Rejection {
		t.Fatalf("another tenant's group = %+v, none at all = %+v; want the same GROUP_NOT_FOUND", crossAfter.Rejection, cross.Rejection)
	}
}

func TestSoftDeletedDevicesAndAttributesAreNotCounted(t *testing.T) {
	api := newSeamApi(t)
	f := standardFleet(t, api, "seam-softdelete")
	v := f.group("quay", `attr["zone"] == "quay"`)
	before := f.digest("quay", v, "dim")
	_, err := api.DeleteDevice(f.ctx, "lamp-7")
	f.must(err, "delete lamp-7")
	_, err = api.DeleteEntityAttribute(f.ctx, "device", "lamp-2", "SHARED", "dim")
	f.must(err, "delete lamp-2 dim")
	d := f.digest("quay", v, "dim")
	if d.MemberCount != before.MemberCount-1 || d.PresentCount != before.PresentCount-1 {
		t.Fatalf("after deletes: %d members, %d present; want %d and %d", d.MemberCount, d.PresentCount,
			before.MemberCount-1, before.PresentCount-1)
	}
	rows, _ := f.walk("quay", v, "dim", 1000)
	for _, r := range rows {
		if r.Token == "lamp-7" || (r.Token == "lamp-2" && r.Present) {
			t.Errorf("a soft-deleted row is observed: %+v", r)
		}
	}
	if rehash(t, rows) != d.Digest {
		t.Error("digest and pages disagree after deletes")
	}
}

func TestOversizeValuesAreReportedByLengthNotHashed(t *testing.T) {
	api := newSeamApi(t)
	f := newFleet(t, api, "seam-oversize")
	f.profile("p", true, model.ConfigurationKey{Key: "doc", ValueType: "STRING"})
	f.deviceType("t", "p")
	// 60 KiB: the configuration document of one device is itself bounded at 64 KiB.
	at, over := strings.Repeat("a", intent.MaxIntentValueBytes), strings.Repeat("b", 60<<10)
	f.device("at", "t", "zone", "STRING", "q", "doc", "STRING", at)
	f.device("over", "t", "zone", "STRING", "q", "doc", "STRING", over)
	f.device("over-1", "t", "zone", "STRING", "q", "doc", "STRING", at+"x")
	v := f.group("g", `attr["zone"] == "q"`)
	d := f.digest("g", v, "doc")
	if d.OversizeCount != 2 || d.PresentCount != 3 {
		t.Fatalf("oversize %d, present %d; want 2 and 3", d.OversizeCount, d.PresentCount)
	}
	rows, _ := f.walk("g", v, "doc", 1000)
	by := map[string]string{}
	for _, r := range rows {
		by[r.Token] = r.ValueDigest
	}
	want, _ := intent.ValueDigest("STRING", at)
	marker := func(n int64) string {
		m, err := intent.OversizeMarker(n)
		f.must(err, "oversize marker")
		return m
	}
	if by["at"] != want || by["over"] != marker(60<<10) || by["over-1"] != marker(intent.MaxIntentValueBytes+1) {
		t.Fatalf("value digests = %v", by)
	}
	if rehash(t, rows) != d.Digest {
		t.Fatal("digest and pages disagree with oversize rows")
	}
	// A change of length moves the digest; a same-length change does not, and does not need to.
	f.attr("over", "doc", "STRING", strings.Repeat("c", 60<<10))
	if same := f.digest("g", v, "doc"); same.Digest != d.Digest {
		t.Error("a same-length oversize change moved the digest; the value was read")
	}
	f.attr("over", "doc", "STRING", strings.Repeat("c", 60<<10-1))
	if moved := f.digest("g", v, "doc"); moved.Digest == d.Digest {
		t.Error("a change of oversize length did not move the digest")
	}
}

func TestADeclaredTypeChangeMovesTheDigestWithNoAttributeWrite(t *testing.T) {
	api := newSeamApi(t)
	f := standardFleet(t, api, "seam-declared")
	v := f.group("quay", `attr["zone"] == "quay"`)
	before := f.digest("quay", v, "dim")
	f.republish("p-long", dimBool)
	after := f.digest("quay", v, "dim")
	if after.Digest == before.Digest {
		t.Fatal("a profile publish that changed dim from LONG to BOOLEAN did not move the digest")
	}
	if got := declarationCounts(after); got["BOOLEAN"] != 4 || got["LONG"] != 0 {
		t.Errorf("declarations after the publish = %v", got)
	}
}

func TestTheDigestIgnoresOtherKeysAndTheDraftSelector(t *testing.T) {
	api := newSeamApi(t)
	f := standardFleet(t, api, "seam-stable")
	v := f.group("quay", `attr["zone"] == "quay"`)
	before := f.digest("quay", v, "dim")
	f.attr("lamp-1", "brightness", "LONG", "7")
	f.setDraftSelector("quay", `attr["zone"] == "east"`)
	if after := f.digest("quay", v, "dim"); after.Digest != before.Digest || after.MemberCount != before.MemberCount {
		t.Fatalf("another key's write or a draft edit moved the digest: %+v -> %+v", before, after)
	}
	// Publishing the draft makes a new version; the old version still means what it meant.
	v2 := f.publishGroup("quay")
	if f.digest("quay", v, "dim").Digest != before.Digest {
		t.Error("publishing a new version changed the old version's answer")
	}
	if d := f.digest("quay", v2, "dim"); d.MemberCount != 1 {
		t.Errorf("the new version has %d members; want lamp-8", d.MemberCount)
	}
	// A write to the observed key does move it.
	f.attr("lamp-1", "dim", "LONG", "39")
	if f.digest("quay", v, "dim").Digest == before.Digest {
		t.Error("a write to the observed key did not move the digest")
	}
}

func TestTargetsThatCannotBeObservedAreRejected(t *testing.T) {
	api := newSeamApi(t)
	f := standardFleet(t, api, "seam-reject")
	static := "static"
	_, err := api.CreateEntityGroup(f.ctx, &model.EntityGroupCreateRequest{Token: "fixed", MemberType: "device", MembershipMode: &static})
	f.must(err, "static group")
	dynamic, sel := "dynamic", `"zone" in attr`
	_, err = api.CreateEntityGroup(f.ctx, &model.EntityGroupCreateRequest{Token: "assets", MemberType: "asset", MembershipMode: &dynamic, Selector: &sel})
	f.must(err, "asset group")
	_, err = api.PublishEntityGroup(f.ctx, "assets", nil, nil, "tester")
	f.must(err, "publish asset group")
	_, err = api.CreateEntityGroup(f.ctx, &model.EntityGroupCreateRequest{Token: "draft", MemberType: "device", MembershipMode: &dynamic, Selector: &sel})
	f.must(err, "draft group")
	v := f.group("quay", `attr["zone"] == "quay"`)
	for name, tc := range map[string]struct {
		target model.IntentSeamTarget
		code   string
	}{
		"unknown":         {model.IntentSeamTarget{GroupToken: "nope", Version: 1}, intentseam.CodeGroupNotFound},
		"static":          {model.IntentSeamTarget{GroupToken: "fixed", Version: 1}, intentseam.CodeGroupNotVersioned},
		"assets":          {model.IntentSeamTarget{GroupToken: "assets", Version: 1}, intentseam.CodeGroupNotADeviceGroup},
		"never published": {model.IntentSeamTarget{GroupToken: "draft", Version: 1}, intentseam.CodeGroupVersionNotFound},
		"no such version": {model.IntentSeamTarget{GroupToken: "quay", Version: v + 1}, intentseam.CodeGroupVersionNotFound},
		"version zero":    {model.IntentSeamTarget{GroupToken: "quay", Version: 0}, intentseam.CodeGroupVersionNotFound},
	} {
		d, err := api.ConfigurationKeyStateDigest(f.ctx, tc.target, "dim")
		if err != nil || !d.Rejected || d.Code != tc.code || d.Digest != "" || d.MemberCount != 0 {
			t.Errorf("%s digest = %+v, %v; want rejected %s", name, d, err, tc.code)
		}
		p, err := api.ConfigurationKeyState(f.ctx, tc.target, "dim", 0, 10)
		if err != nil || !p.Rejected || p.Code != tc.code || len(p.Rows) != 0 {
			t.Errorf("%s page = %+v, %v; want rejected %s", name, p, err, tc.code)
		}
		x, err := api.GroupTargetsIntersect(f.ctx, model.IntentSeamTarget{GroupToken: "quay", Version: v}, tc.target)
		if err != nil || !x.Rejected || x.Code != tc.code || x.Intersects {
			t.Errorf("%s intersect = %+v, %v; want rejected %s", name, x, err, tc.code)
		}
	}
	if _, err := api.ConfigurationKeyStateDigest(context.Background(), model.IntentSeamTarget{GroupToken: "quay", Version: v}, "dim"); !errors.Is(err, core.ErrNoTenant) {
		t.Errorf("no tenant = %v; want ErrNoTenant before any SQL", err)
	}
}

func TestTargetsIntersect(t *testing.T) {
	api := newSeamApi(t)
	f := standardFleet(t, api, "seam-intersect")
	quay := f.group("quay", `attr["zone"] == "quay"`)
	east := f.group("east", `attr["zone"] == "east"`)
	lamps := f.group("lamps", `"dim" in attr`)
	q := func(a string, av int32, b string, bv int32) bool {
		t.Helper()
		x, err := api.GroupTargetsIntersect(f.ctx, model.IntentSeamTarget{GroupToken: a, Version: av}, model.IntentSeamTarget{GroupToken: b, Version: bv})
		f.must(err, "intersect")
		if x.Rejected {
			t.Fatalf("%s ∩ %s rejected: %+v", a, b, x.Rejection)
		}
		return x.Intersects
	}
	if !q("quay", quay, "lamps", lamps) {
		t.Error("two different groups sharing devices do not intersect")
	}
	if q("quay", quay, "east", east) {
		t.Error("disjoint groups intersect")
	}
	// Two versions of one group: v1 quay, v2 east; disjoint. v3 everything; intersects v1.
	f.setDraftSelector("quay", `attr["zone"] == "east"`)
	v2 := f.publishGroup("quay")
	if q("quay", quay, "quay", v2) {
		t.Error("two disjoint versions of one group intersect")
	}
	f.setDraftSelector("quay", `"zone" in attr`)
	v3 := f.publishGroup("quay")
	if !q("quay", quay, "quay", v3) || !q("quay", v2, "quay", v3) {
		t.Error("overlapping versions of one group do not intersect")
	}
	// A deleted shared device ends the intersection.
	_, err := api.DeleteDevice(f.ctx, "lamp-8")
	f.must(err, "delete")
	if q("east", east, "lamps", lamps) {
		t.Error("a soft-deleted device still makes two targets intersect")
	}
}

func TestPagesStayFixedSizeWithMaximalValues(t *testing.T) {
	api := newSeamApi(t)
	f := newFleet(t, api, "seam-pagebytes")
	f.profile("p", true, model.ConfigurationKey{Key: "doc", ValueType: "STRING"})
	f.deviceType("t", "p")
	f.device("d0", "t", "zone", "STRING", "q")
	// 1,000 devices, each holding a 64 KiB value of characters JSON escapes, seeded directly.
	escaped := strings.Repeat("\"\\\n\x01<>&", 64<<10/8)
	f.exec(`INSERT INTO devices (created_at, updated_at, tenant_id, token, device_type_id)
		SELECT now(), now(), ?, 'big-' || lpad(g::text, 120, '0'), (SELECT id FROM device_types WHERE tenant_id = ? AND token = 't')
		FROM generate_series(1, 1000) g`, f.tenant, f.tenant)
	f.exec(`INSERT INTO entity_attributes (created_at, updated_at, tenant_id, entity_type, entity_id, scope, attr_key, value_type, value, last_updated)
		SELECT now(), now(), d.tenant_id, 'device', d.id, 'SHARED', k.key, 'STRING', k.v, now()
		FROM devices d CROSS JOIN (VALUES ('zone', 'q'), ('doc', ?)) AS k(key, v)
		WHERE d.tenant_id = ? AND d.token LIKE 'big-%'`, escaped, f.tenant)
	analyze(f)
	v := f.group("g", `attr["zone"] == "q"`)
	page, err := api.ConfigurationKeyState(f.ctx, model.IntentSeamTarget{GroupToken: "g", Version: v}, "doc", 0, 1000)
	f.must(err, "page")
	if len(page.Rows) != 1000 {
		t.Fatalf("a 1,000-row page held %d rows", len(page.Rows))
	}
	encoded, err := json.Marshal(page.Rows)
	f.must(err, "encode the page")
	if len(encoded) > intentseam.PageByteBudget || len(encoded) >= svcclient.MaxResponseBytes {
		t.Fatalf("a page of 1,000 maximal escaped values encodes to %d bytes; the budget is %d and the transport cap %d",
			len(encoded), intentseam.PageByteBudget, svcclient.MaxResponseBytes)
	}
	// The byte budget ends a page early with a cursor, and the walk still visits every row.
	tight := model.NewApi(seamManager(t))
	tight.IntentSeam = intentseam.NewWithBudgets(intentseam.BusyWait, nil, 20<<10)
	early, err := tight.ConfigurationKeyState(f.ctx, model.IntentSeamTarget{GroupToken: "g", Version: v}, "doc", 0, 1000)
	f.must(err, "tight page")
	if len(early.Rows) == 0 || len(early.Rows) >= 1000 || early.NextCursor != early.Rows[len(early.Rows)-1].DeviceID {
		t.Fatalf("the byte budget did not end the page early with a cursor: %d rows, cursor %d", len(early.Rows), early.NextCursor)
	}
	tf := &fleet{t: t, api: tight, ctx: f.ctx, tenant: f.tenant}
	rows, _ := tf.walk("g", v, "doc", 1000)
	if len(rows) != 1001 {
		t.Fatalf("a budget-bounded walk visited %d rows; want 1001", len(rows))
	}
	if rehash(t, rows) != f.digest("g", v, "doc").Digest {
		t.Fatal("a budget-bounded walk does not hash to the digest")
	}
}

func TestTheDigestCoversZeroOneAndManyMembers(t *testing.T) {
	api := newSeamApi(t)
	f := newFleet(t, api, "seam-sizes")
	f.profile("p", true, dimLong)
	f.deviceType("t", "p")
	f.device("solo", "t", "zone", "STRING", "one", "dim", "LONG", "40")
	none := f.group("none", `attr["zone"] == "nowhere"`)
	one := f.group("one", `attr["zone"] == "one"`)
	d0 := f.digest("none", none, "dim")
	if d0.Rejected || d0.MemberCount != 0 || d0.Digest != rehash(t, nil) || len(d0.Declarations) != 0 {
		t.Fatalf("an empty target = %+v; want zero members and the empty digest", d0)
	}
	d1 := f.digest("one", one, "dim")
	rows, _ := f.walk("one", one, "dim", 1000)
	if d1.MemberCount != 1 || rehash(t, rows) != d1.Digest {
		t.Fatalf("a one-member target = %+v", d1)
	}
	n := 1000
	if os.Getenv("DC_IT_INTENT_COST") != "" {
		n = intent.MaxDevicesPerIntent
	}
	seedMany(f, "many", n, "40")
	many := f.group("many", `attr["zone"] == "many"`)
	dn := f.digest("many", many, "dim")
	rows, _ = f.walk("many", many, "dim", intentseam.MaxPageRows)
	if dn.MemberCount != int64(n) || len(rows) != n || rehash(t, rows) != dn.Digest {
		t.Fatalf("a %d-member target: memberCount %d, %d paged rows, digests equal %v", n, dn.MemberCount, len(rows), rehash(t, rows) == dn.Digest)
	}
}

// seedMany inserts n devices of type t in zone, each holding dim = value, directly.
func seedMany(f *fleet, zone string, n int, value string) {
	f.t.Helper()
	f.exec(`INSERT INTO devices (created_at, updated_at, tenant_id, token, device_type_id)
		SELECT now(), now(), ?, ? || '-' || g, (SELECT id FROM device_types WHERE tenant_id = ? AND token = 't')
		FROM generate_series(1, ?) g`, f.tenant, zone, f.tenant, n)
	f.exec(`INSERT INTO entity_attributes (created_at, updated_at, tenant_id, entity_type, entity_id, scope, attr_key, value_type, value, last_updated)
		SELECT now(), now(), d.tenant_id, 'device', d.id, 'SHARED', k.key, k.vt, k.v, now()
		FROM devices d CROSS JOIN (VALUES ('zone', 'STRING', ?), ('dim', 'LONG', ?)) AS k(key, vt, v)
		WHERE d.tenant_id = ? AND d.token LIKE ? || '-%'`, zone, value, f.tenant, zone)
	analyze(f)
}

// analyze refreshes the planner's statistics after a bulk seed, as autovacuum would on a live
// install. Without it the planner plans a 20,000-row tenant as an empty one, and a correlated
// probe per member turns a sub-second digest into a statement timeout (which is the seam's
// hard stop doing its job, not a plan worth measuring).
func analyze(f *fleet) {
	f.t.Helper()
	f.must(f.api.RDB.Database.Exec(`ANALYZE devices, entity_attributes`).Error, "analyze")
}

func TestATargetOverTheIntentCeilingIsRefusedNotTruncated(t *testing.T) {
	api := newSeamApi(t)
	f := newFleet(t, api, "seam-ceiling")
	f.profile("p", true, dimLong)
	f.deviceType("t", "p")
	// The production bound is intent.MaxDevicesPerIntent; the cost gate exercises it at its real
	// size. Here a seam with a bound of 10 runs the same statement over 11 members.
	bound, seeded := 10, 11
	seam := intentseam.NewWithBudgets(intentseam.BusyWait, nil, 0)
	intentseam.SetMaxMembers(seam, bound)
	if os.Getenv("DC_IT_INTENT_COST") != "" {
		bound, seeded = intent.MaxDevicesPerIntent, intent.MaxDevicesPerIntent+1
		seam = intentseam.New()
	}
	seedMany(f, "big", seeded, "40")
	v := f.group("big", `attr["zone"] == "big"`)
	capped := model.NewApi(seamManager(t))
	capped.IntentSeam = seam
	target := model.IntentSeamTarget{GroupToken: "big", Version: v}
	d, err := capped.ConfigurationKeyStateDigest(f.ctx, target, "dim")
	f.must(err, "digest")
	if !d.Rejected || d.Code != intentseam.CodeTargetTooLarge || d.Digest != "" || d.MemberCount != 0 {
		t.Fatalf("a %d-member target over a bound of %d = %+v; want TARGET_TOO_LARGE, never a truncated count", seeded, bound, d)
	}
	f.exec(`UPDATE devices SET deleted_at = now() WHERE tenant_id = ? AND token = 'big-1'`, f.tenant)
	d, err = capped.ConfigurationKeyStateDigest(f.ctx, target, "dim")
	f.must(err, "digest")
	if d.Rejected || d.MemberCount != int64(bound) {
		t.Fatalf("a target exactly at the bound = %+v", d)
	}
}

func TestAStatementOverItsTimeoutGivesNoAnswer(t *testing.T) {
	api := newSeamApi(t)
	f := newFleet(t, api, "seam-timeout")
	f.profile("p", true, dimLong)
	f.deviceType("t", "p")
	seedMany(f, "slow", 20_000, "40")
	v := f.group("slow", `attr["zone"] == "slow"`)
	slow := model.NewApi(seamManager(t))
	slow.IntentSeam = intentseam.NewWithBudgets(intentseam.BusyWait, map[string]time.Duration{
		intentseam.OpDigest: time.Millisecond, intentseam.OpPage: time.Millisecond, intentseam.OpIntersect: time.Millisecond,
	}, 0)
	target := model.IntentSeamTarget{GroupToken: "slow", Version: v}
	isTimeout := func(err error) bool {
		var se *intentseam.Error
		return errors.As(err, &se) && se.Code == intentseam.CodeSeamTimeout
	}
	if d, err := slow.ConfigurationKeyStateDigest(f.ctx, target, "dim"); !isTimeout(err) || d != nil {
		t.Errorf("a digest over its timeout = %+v, %v; want SEAM_TIMEOUT and no answer", d, err)
	}
	if p, err := slow.ConfigurationKeyState(f.ctx, target, "dim", 0, 1000); !isTimeout(err) || p != nil {
		t.Errorf("a page over its timeout = %+v, %v; want SEAM_TIMEOUT and no answer", p, err)
	}
	if x, err := slow.GroupTargetsIntersect(f.ctx, target, model.IntentSeamTarget{GroupToken: "slow", Version: v}); err == nil && x != nil {
		// EXISTS stops at the first shared row, so it may finish inside a millisecond. What
		// matters is that it either answers or times out, never answers partially.
		if x.Rejected {
			t.Errorf("an intersect under a tight timeout was rejected: %+v", x)
		}
	} else if !isTimeout(err) {
		t.Errorf("an intersect under a tight timeout = %v", err)
	}
	// The same statements under the real budgets answer.
	if d := f.digest("slow", v, "dim"); d.Rejected || d.MemberCount != 20_000 {
		t.Fatalf("the digest under its real budget = %+v", d)
	}
}

func TestTheSeamIsReadOnly(t *testing.T) {
	api := newSeamApi(t)
	f := newFleet(t, api, "seam-readonly")
	f.profile("p", true, dimLong)
	f.deviceType("t", "p")
	err := api.IntentSeam.Run(f.ctx, api.RDB.DB(f.ctx), intentseam.OpPage, func(tx *gorm.DB) (bool, error) {
		return false, tx.Exec(`UPDATE devices SET name = 'x' WHERE tenant_id = ?`, f.tenant).Error
	})
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("a write inside a seam statement = %v; want a read-only transaction refusal", err)
	}
}

func TestSeamStatementsShareTheLimiter(t *testing.T) {
	api := newSeamApi(t)
	f := standardFleet(t, api, "seam-busy")
	v := f.group("quay", `attr["zone"] == "quay"`)
	s := intentseam.NewWithBudgets(50*time.Millisecond, nil, 0)
	hold, entered := make(chan struct{}), make(chan struct{}, 8)
	intentseam.SetHeldHook(s, func(string) { entered <- struct{}{}; <-hold })
	busy := model.NewApi(seamManager(t))
	busy.IntentSeam = s
	target := model.IntentSeamTarget{GroupToken: "quay", Version: v}
	errs := make(chan error, 8)
	for range 2 {
		go func() { _, err := busy.ConfigurationKeyStateDigest(f.ctx, target, "dim"); errs <- err }()
	}
	for range 2 {
		go func() { _, err := busy.ConfigurationKeyState(f.ctx, target, "dim", 0, 10); errs <- err }()
	}
	for range 4 {
		<-entered
	}
	isBusy := func(err error) bool {
		var se *intentseam.Error
		return errors.As(err, &se) && se.Code == intentseam.CodeSeamBusy
	}
	if _, err := busy.ConfigurationKeyState(f.ctx, target, "dim", 0, 10); !isBusy(err) {
		t.Errorf("a fifth statement = %v; want SEAM_BUSY", err)
	}
	if _, err := busy.PreviewIntentBulkChange(f.ctx, model.FleetKindConfigSet, &model.FleetConfigSetIntent{Key: "dim", ValueType: "LONG", Value: ptr("40")}, []string{"lamp-1"}); !isBusy(err) {
		t.Errorf("a preview with the seam full = %v; want SEAM_BUSY", err)
	}
	close(hold)
	for range 4 {
		if err := <-errs; err != nil {
			t.Errorf("a held statement failed: %v", err)
		}
	}
}

func ptr(s string) *string { return &s }

// Every seam statement runs with its timeout set and with custom plans forced, in a read-only
// snapshot: what the statement sees, not what a test of the result could infer.
func TestSeamStatementsRunUnderTheirSettings(t *testing.T) {
	api := newSeamApi(t)
	ctx := core.WithTenant(context.Background(), "seam-settings")
	for op, want := range map[string]string{intentseam.OpDigest: "20s", intentseam.OpPage: "5s", intentseam.OpIntersect: "20s", intentseam.OpPreview: "30s"} {
		var got struct {
			Timeout  string
			PlanMode string
			ReadOnly string
			Iso      string
		}
		err := api.IntentSeam.Run(ctx, api.RDB.DB(ctx), op, func(tx *gorm.DB) (bool, error) {
			return false, tx.Raw(`SELECT current_setting('statement_timeout') AS timeout, current_setting('plan_cache_mode') AS plan_mode,
				current_setting('transaction_read_only') AS read_only, current_setting('transaction_isolation') AS iso`).Scan(&got).Error
		})
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		if got.Timeout != want || got.PlanMode != "force_custom_plan" || got.ReadOnly != "on" || got.Iso != "repeatable read" {
			t.Errorf("%s runs under %+v; want timeout %s, force_custom_plan, read only, repeatable read", op, got, want)
		}
	}
}
