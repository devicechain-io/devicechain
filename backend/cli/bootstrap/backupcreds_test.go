// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeBackupFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "backup.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const goodBackupFile = `{
  "endpointUrl": "https://s3.example.com",
  "bucketRdb": "dc-rdb-archive",
  "bucketTsdb": "dc-tsdb-archive",
  "accessKeyId": "AKIAEXAMPLE",
  "secretAccessKey": "not-a-real-secret-value"
}`

func TestASuppliedDestinationIsReadWhole(t *testing.T) {
	d, err := ParseBackupDestination(writeBackupFile(t, goodBackupFile))
	if err != nil {
		t.Fatalf("parsing a well-formed file: %v", err)
	}
	if !d.Configured() {
		t.Fatal("a parsed destination reports itself unconfigured")
	}
	if d.BucketRdb == d.BucketTsdb || d.EndpointURL == "" || d.AccessKeyID == "" {
		t.Errorf("fields did not survive the parse: %+v", *d)
	}
}

// No flag means the in-cluster store, not an error.
func TestNoBackupFileMeansTheInClusterStore(t *testing.T) {
	d, err := ParseBackupDestination("")
	if err != nil || d != nil {
		t.Fatalf("an absent flag was not treated as 'use the in-cluster store': %v %v", d, err)
	}
	if d.Configured() {
		t.Error("a nil destination reports itself configured")
	}
}

// 🔴 EVERY FIELD IS REQUIRED, AND THE REASON IS THE SAME FOR ALL OF THEM: an archive
// that cannot be reached does not fail loudly. The database keeps accepting writes
// and WAL simply stops being shipped.
func TestEveryFieldOfASuppliedDestinationIsRequired(t *testing.T) {
	for _, field := range []string{"endpointUrl", "bucketRdb", "bucketTsdb", "accessKeyId", "secretAccessKey"} {
		t.Run(field, func(t *testing.T) {
			body := strings.Replace(goodBackupFile, `"`+field+`": "`, `"`+field+`": "`, 1)
			// blank just this field
			lines := strings.Split(body, "\n")
			for i, l := range lines {
				if strings.Contains(l, `"`+field+`"`) {
					lines[i] = `  "` + field + `": "",`
					if strings.HasSuffix(strings.TrimSpace(l), `"`) {
						lines[i] = `  "` + field + `": ""`
					}
				}
			}
			_, err := ParseBackupDestination(writeBackupFile(t, strings.Join(lines, "\n")))
			if err == nil {
				t.Fatalf("a destination with no %s was accepted; it would archive nowhere", field)
			}
			if !strings.Contains(err.Error(), field) {
				t.Errorf("the refusal does not name the missing field: %v", err)
			}
		})
	}
}

// 🔴 ONE BUCKET FOR BOTH STORES IS NOT A TYPO WITH A COSMETIC COST. Each archives
// over the other's prefix, and the damage is invisible until a restore reads a base
// backup belonging to the wrong database.
func TestTheTwoStoresMayNotShareOneBucket(t *testing.T) {
	body := strings.Replace(goodBackupFile, `"bucketTsdb": "dc-tsdb-archive"`, `"bucketTsdb": "dc-rdb-archive"`, 1)
	_, err := ParseBackupDestination(writeBackupFile(t, body))
	if err == nil {
		t.Fatal("both stores were allowed to archive to one bucket")
	}
	if !strings.Contains(err.Error(), "separate buckets") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

// An unknown key is refused rather than ignored.
//
// 🔴 THE OBVIOUS VERSION OF THIS TEST PASSES WITHOUT THE CHECK IT NAMES. Misspelling
// `secretAccessKey` leaves that field empty, so the REQUIRED-field check refuses the
// file and the test goes green with DisallowUnknownFields deleted — measured, as a
// surviving mutant. What actually exercises it is an extra field alongside a
// complete set, because then nothing else has anything to complain about.
//
// The case is real rather than contrived: `region` is a field every other S3 client
// takes, so it is exactly what an operator would add — and silently ignoring it
// would leave them believing they had configured something they had not.
func TestAnUnknownKeyIsRefusedRatherThanIgnored(t *testing.T) {
	body := strings.Replace(goodBackupFile,
		`"accessKeyId": "AKIAEXAMPLE"`,
		`"region": "us-east-1",
  "accessKeyId": "AKIAEXAMPLE"`, 1)
	_, err := ParseBackupDestination(writeBackupFile(t, body))
	if err == nil {
		t.Fatal("an unknown field was accepted and ignored, so an operator who configured " +
			"something this file does not support would be told nothing")
	}
	if !strings.Contains(err.Error(), "region") {
		t.Errorf("the refusal does not name the field it did not recognise: %v", err)
	}
}

// The object store's length floor, applied to a SUPPLIED value for the first time —
// the OpenTofu variable validation that used to enforce it is retired with the
// variable, and a short key takes the store into a crash loop whose first visible
// symptom is that WAL stopped being archived.
func TestASuppliedSecretKeyIsHeldToTheStoresFloor(t *testing.T) {
	body := strings.Replace(goodBackupFile, `"secretAccessKey": "not-a-real-secret-value"`, `"secretAccessKey": "short"`, 1)
	_, err := ParseBackupDestination(writeBackupFile(t, body))
	if err == nil {
		t.Fatal("a secretAccessKey below the object store's floor was accepted")
	}
}

// An endpoint the archiver cannot dial fails here rather than at the first base backup.
func TestAMalformedEndpointIsRefusedBeforeACluster(t *testing.T) {
	for _, bad := range []string{"s3.example.com", "ftp://s3.example.com", "https://"} {
		body := strings.Replace(goodBackupFile, "https://s3.example.com", bad, 1)
		if _, err := ParseBackupDestination(writeBackupFile(t, body)); err == nil {
			t.Errorf("endpointUrl %q was accepted", bad)
		}
	}
}

// 🔴 THE TWO DESTINATIONS ARE MUTUALLY EXCLUSIVE, AND THE PLAN HAS TO PICK ONE. An
// external destination provisions no object store, so a run that wrote
// dc-object-store-credentials would be writing a credential with no MinIO to
// authenticate against — while the Secret the archiver actually presents went
// unwritten.
func TestAnExternalDestinationWritesTheArchiversSecretAndNotTheStores(t *testing.T) {
	d, err := ParseBackupDestination(writeBackupFile(t, goodBackupFile))
	if err != nil {
		t.Fatal(err)
	}
	st := &State{Instance: testInstance, BackupDestination: d}
	set, err := resolveCredentials(t.Context(), nil, withDryRun(st), liveArchiveState{})
	if err != nil {
		t.Fatal(err)
	}

	names := map[string]bool{}
	for _, s := range planOwnedSecrets(st, set) {
		names[s.Name] = true
	}
	if !names[backupCredentialsSecretName] {
		t.Error("no dc-backup-credentials was planned, so the archiver has nothing to present")
	}
	if names[objectStoreName+"-credentials"] {
		t.Error("an in-cluster object store credential was planned for an EXTERNAL destination, " +
			"where no object store exists to read it")
	}
	if set.ObjectStoreSecret != "" {
		t.Error("entropy was minted for an object store this run does not provision")
	}
}

// ...and the in-cluster case is unchanged: its store's credentials are minted, and
// no archiver Secret is written because there is no external archiver.
func TestTheInClusterDestinationStillMintsItsOwnStore(t *testing.T) {
	st := &State{Instance: testInstance}
	set, err := resolveCredentials(t.Context(), nil, withDryRun(st), liveArchiveState{})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, s := range planOwnedSecrets(st, set) {
		names[s.Name] = true
	}
	if !names[objectStoreName+"-credentials"] {
		t.Error("the in-cluster store lost its credentials")
	}
	if names[backupCredentialsSecretName] {
		t.Error("an external archiver Secret was written for an in-cluster destination")
	}
}

// withDryRun keeps these plan tests off the cluster: they are about WHICH Secrets a
// destination implies, not about reuse.
func withDryRun(st *State) *State { st.DryRun = true; return st }

// 🔴 A DRY RUN MUST PREDICT WHAT THE REAL RUN DOES, AND THIS ONE PREDICTED THE
// OPPOSITE. The two report values are read back from the apply's outputs — reality
// rather than our own request, which is right — but a dry run never applies, so both
// were empty and every rehearsal printed "Backups: NONE". Including, after this
// slice, a rehearsal that had just been handed an off-site destination.
func TestADryRunPredictsTheBackupOutcomeItWouldProduce(t *testing.T) {
	d, err := ParseBackupDestination(writeBackupFile(t, goodBackupFile))
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		st          *State
		wantEnabled string
		wantOffsite string
	}{
		"off-site destination supplied": {
			st: &State{DryRun: true, BackupDestination: d}, wantEnabled: "true", wantOffsite: "true",
		},
		"in-cluster by default": {
			st: &State{DryRun: true}, wantEnabled: "true", wantOffsite: "false",
		},
		"no backups at all": {
			st: &State{DryRun: true, NoCNPG: true}, wantEnabled: "", wantOffsite: "",
		},
	} {
		t.Run(name, func(t *testing.T) {
			st := tc.st
			st.Values = map[string]string{}
			if databaseBackupsEnabled(st) {
				st.Values[databaseBackupsKey] = "true"
				st.Values[databaseBackupOffsiteKey] = fmt.Sprintf("%t", backupsAreExternal(st))
			}
			if got := st.Values[databaseBackupsKey]; got != tc.wantEnabled {
				t.Errorf("databaseBackups predicted %q, want %q", got, tc.wantEnabled)
			}
			if got := st.Values[databaseBackupOffsiteKey]; got != tc.wantOffsite {
				t.Errorf("databaseBackupOffsite predicted %q, want %q", got, tc.wantOffsite)
			}
		})
	}
}
