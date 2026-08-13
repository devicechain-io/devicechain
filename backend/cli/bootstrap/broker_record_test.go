// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// withRecordDir points the record at a temp directory. The production resolver reads
// $HOME, and every test in this package runs with Instance "prod".
func withRecordDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig := brokerRecordPathFor
	t.Cleanup(func() { brokerRecordPathFor = orig })
	brokerRecordPathFor = func(instance string) (string, error) {
		return filepath.Join(dir, instance+"-"+brokerRecordFile), nil
	}
	return dir
}

// writeAt is writeBrokerRecord's body against an explicit path, so the file-level tests
// do not need instanceStateDir (which insists on $HOME).
func writeAt(t *testing.T, path string, rec brokerRecord) {
	t.Helper()
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, raw, stateFileMode); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func sampleRecord(instance string) brokerRecord {
	return brokerRecord{
		Instance:        instance,
		Written:         time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC),
		IssuerSeed:      "SAAHXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX",
		ServicePassword: "service-plaintext",
		SysPassword:     "sys-plaintext",
	}
}

// 🔴 EVERY WAY THE RECORD CAN BE WRONG MEANS "NO RECORD", NEVER AN ERROR. This read was
// added for a rare recovery, and the standing objection to adding it at all — the same one
// DeployedBrokerHashes was written to answer — is that a read must not give an ORDINARY
// re-run a new way to fail. Each case here is a way a file can be bad; all of them fall
// through to minting, which is exactly the behaviour that existed before the record did.
func TestABadRecordReadsAsNoRecord(t *testing.T) {
	dir := withRecordDir(t)
	path := filepath.Join(dir, "prod-"+brokerRecordFile)

	cases := []struct {
		name  string
		setup func()
	}{
		{"missing", func() { _ = os.Remove(path) }},
		{"empty", func() { writeRaw(t, path, "") }},
		{"truncated mid-write", func() { writeRaw(t, path, `{"instance":"prod","issuerSee`) }},
		{"not json at all", func() { writeRaw(t, path, "SAAHNOTJSON") }},
		{"a different instance", func() {
			rec := sampleRecord("staging")
			writeAt(t, path, rec)
		}},
		{"no seed", func() {
			rec := sampleRecord("prod")
			rec.IssuerSeed = ""
			writeAt(t, path, rec)
		}},
		{"no service password", func() {
			rec := sampleRecord("prod")
			rec.ServicePassword = ""
			writeAt(t, path, rec)
		}},
		// 🔴 THE ONE CASE THE FIELD GUARD BELOW CANNOT CATCH, and the reason the decode
		// error is checked rather than ignored. A syntax error is caught before anything
		// is populated, so the guard sees a zero record either way — but a document that
		// is syntactically fine and wrong only in a LATER field's type populates
		// everything up to the failure. Here instance, seed and service password all
		// arrive; only sysPassword is lost. Accepting that silently mints a fresh SYS
		// password against a broker that is enforcing the old one.
		{"valid json, wrong type on a later field", func() {
			writeRaw(t, path, `{"instance":"prod","issuerSeed":"SAAHX","servicePassword":"p","sysPassword":7}`)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup()
			if got := readBrokerRecord("prod"); got != nil {
				t.Fatalf("a %s record was accepted: %+v", tc.name, got)
			}
		})
	}

	// 🔑 THE COUNTERWEIGHT. Without it every case above passes against a reader that
	// returns nil unconditionally, which would silently disable the whole feature.
	writeAt(t, path, sampleRecord("prod"))
	got := readBrokerRecord("prod")
	if got == nil {
		t.Fatal("a well-formed record was rejected, so the cases above prove nothing")
	}
	if got.IssuerSeed != sampleRecord("prod").IssuerSeed {
		t.Fatalf("issuer seed round-tripped as %q", got.IssuerSeed)
	}
}

func writeRaw(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), stateFileMode); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// 🔴 A TORN RECORD READS AS ABSENT, SO THE WRITE HAS TO BE ALL-OR-NOTHING — otherwise a
// crash partway through leaves the retry that most needs the record minting instead, which
// is the defect this file exists to fix, reintroduced by its own fix. The observable proof
// that the write is not in-place: nothing but the final rename ever touches the real name,
// so an existing record is intact until the moment it is replaced whole.
func TestTheRecordIsReplacedWholeAndStaysPrivate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows

	first := sampleRecord("prod")
	if err := writeBrokerRecord("prod", first); err != nil {
		t.Fatalf("first write: %v", err)
	}
	path := filepath.Join(home, ".devicechain", "prod", brokerRecordFile)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != stateFileMode {
		t.Fatalf("record mode is %v, want %v — it holds an issuer seed in cleartext",
			info.Mode().Perm(), stateFileMode)
	}

	// A rewrite must land the new content AND keep the mode. Loosening it by hand first is
	// what makes that assertion mean something: os.WriteFile's mode applies only when it
	// CREATES the file, so an in-place rewrite would keep the 0644 set here. What restores
	// it is the rename of a fresh temp, not the chmod inside the writer — mutating that
	// chmod away changes nothing, because os.CreateTemp already creates at 0600.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	second := sampleRecord("prod")
	second.ServicePassword = "rotated"
	if err := writeBrokerRecord("prod", second); err != nil {
		t.Fatalf("second write: %v", err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatalf("stat after rewrite: %v", err)
	}
	if info.Mode().Perm() != stateFileMode {
		t.Fatalf("a rewrite left the record at %v, want %v", info.Mode().Perm(), stateFileMode)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got brokerRecord
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("the rewritten record is not valid json: %v", err)
	}
	if got.ServicePassword != "rotated" {
		t.Fatalf("rewrite did not replace the content: %+v", got)
	}

	// No temp file left behind: a stray *.json.NNNN beside the record is a second
	// cleartext copy of an issuer seed that nothing will ever clean up.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("the write left %d files behind (%s); a leftover temp is a second copy of the seed",
			len(entries), strings.Join(names, ", "))
	}
}

// 🔴 DESTROY MUST TAKE THE RECORD WITH IT. A full teardown removes the instance directory
// but SPARES anything whose name looks like escrow material, so a record named to sit
// "next to the escrow" would survive — and a same-name rebuild would then find no instance
// config, a present record, and silently resurrect the destroyed instance's broker
// credentials. This is a one-line rule that is invisible at the call site, which is exactly
// why it needs a test rather than a comment.
func TestTheRecordsNameIsNotSparedByDestroy(t *testing.T) {
	if looksLikeEscrow(brokerRecordFile) {
		t.Fatalf("%q is treated as escrow material, so dcctl destroy would leave it behind and a "+
			"rebuild under the same instance name would inherit a dead instance's credentials",
			brokerRecordFile)
	}
	// The counterweight: the exemption is real, so this test can fail.
	if !looksLikeEscrow("prod-rootkey.escrow") {
		t.Fatal("looksLikeEscrow no longer spares escrow artifacts, so the check above proves nothing")
	}
}

// 🔴 THE PREMISE THE WHOLE FILE RESTS ON: the seed and the plaintexts never reach OpenTofu,
// which is why the cluster cannot be asked for them after step 3 and why they have to be
// written down before it. That is a property of what infraVars passes and of nothing else —
// one convenience away from changing, and if it ever does, this record stops being the only
// copy and its design should be revisited rather than quietly outgrown.
func TestOpenTofuNeverReceivesTheSeedOrThePlaintexts(t *testing.T) {
	st := &State{
		Instance:    "prod",
		KubeContext: "kind-devicechain",
		Values: map[string]string{
			"natsCalloutIssuerPublic":   "AAKZPUBLIC",
			"natsCalloutIssuerSeed":     "SAAHSEEDSEEDSEED",
			"natsServicePassword":       "service-plaintext",
			"natsServicePasswordBcrypt": "$2a$11$servicehash",
			"natsSysPassword":           "sys-plaintext",
			"natsSysPasswordBcrypt":     "$2a$11$syshash",
		},
	}
	joined := strings.Join(infraVars(st), "\n")
	for _, secret := range []string{"SAAHSEEDSEEDSEED", "service-plaintext", "sys-plaintext"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("infraVars now passes %q to OpenTofu. That is not automatically wrong, but the "+
				"broker credential record exists BECAUSE nothing recoverable from the cluster can "+
				"be turned back into it — revisit broker_record.go before relaxing this.", secret)
		}
	}
	// The counterweight: the hashes and the public key DO cross, so a test that simply
	// found nothing would be measuring an empty var list.
	for _, expected := range []string{"AAKZPUBLIC", "$2a$11$servicehash"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("infraVars no longer passes %q, so this test is reading the wrong thing", expected)
		}
	}
}

// 🔴 THE RECORD HAS TO BE DURABLE BEFORE THE BROKER IS CONFIGURED, AND "STEP 1" IS NOT THE
// INVARIANT — "before tofu consumes the credentials" is. Nothing in this repo pins the
// pipeline's order, so both halves are asserted: that render really does precede the infra
// apply, and that a run dying at the apply leaves the bridge behind. The second is the
// observed incident replayed.
func TestARunThatDiesAtTheInfraApplyStillLeavesTheRecord(t *testing.T) {
	steps := NewDefaultPipeline().Steps
	renderAt, infraAt := -1, -1
	for i, s := range steps {
		switch reflect.ValueOf(s.Run).Pointer() {
		case reflect.ValueOf(stepRenderConfig).Pointer():
			renderAt = i
		case reflect.ValueOf(stepInfraApply).Pointer():
			infraAt = i
		}
	}
	if renderAt < 0 || infraAt < 0 {
		t.Fatalf("could not find both steps in the default pipeline (render=%d infra=%d)", renderAt, infraAt)
	}
	if renderAt >= infraAt {
		t.Fatalf("stepRenderConfig runs at %d and stepInfraApply at %d: the credentials would reach "+
			"the broker before anything recorded them", renderAt, infraAt)
	}

	withDeployedInstance(t, nil, nil)
	stored := withBrokerRecord(t, nil)
	st := &State{Instance: "prod", BuildImages: true, Values: map[string]string{}}

	failing := Pipeline{Steps: []Step{
		steps[renderAt],
		{Name: "Apply infrastructure", Run: func(context.Context, *State) error {
			return context.DeadlineExceeded
		}},
	}}
	if err := failing.Run(t.Context(), st); err == nil {
		t.Fatal("the fixture must fail at the infra step, or it proves nothing about a dead run")
	}

	if len(*stored) != 1 {
		t.Fatalf("the run stored %d records; a bootstrap that dies at the apply must leave exactly "+
			"one bridge copy behind", len(*stored))
	}
	rec := (*stored)[0]
	if rec.IssuerSeed != st.Values["natsCalloutIssuerSeed"] ||
		rec.ServicePassword != st.Values["natsServicePassword"] ||
		rec.SysPassword != st.Values["natsSysPassword"] {
		t.Fatalf("the record does not hold what the broker would have been configured with: %+v", rec)
	}
	if rec.Instance != "prod" {
		t.Fatalf("the record was stamped for instance %q", rec.Instance)
	}
}
