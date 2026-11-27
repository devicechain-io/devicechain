// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/devicechain-io/dc-microservice/config"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installflow/testkit"
	"github.com/devicechain-io/dcctl/internal/installjson"
	"github.com/devicechain-io/dcctl/internal/installplain"
	"github.com/devicechain-io/dcctl/internal/installtui"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// 🔴 EVERY PRINT SITE OF A USER-SUPPLIED URL SHOWS IT THROUGH installflow.RedactURL.
//
// Each credential below carries the marker "ZX" (in any case), and nothing else in
// the fixtures does: an output holding "zx" in any case has printed a credential, or
// a piece of one. The URLs cover userinfo user:pass, user-only and percent-encoded,
// query tokens (X-Amz-Signature, sig, token), fragment tokens raw and escaped, an
// IPv6 host, and the s3, gs, https and oci schemes.
var credentialURLs = []string{
	"s3://dcuserZX:dcPwZX@bucket/archive",
	"s3://u:zx@bucket/archive",
	"https://ghp_TokZX@host.example/archive",
	"gs://dc%75serZX%3Ap%40ssZX@bucket/archive",
	"https://bucket.s3.example/archive?X-Amz-Credential=AKIDZX&X-Amz-Signature=dcSigZX",
	"https://acct.blob.example/c/archive?sv=2024&sig=dcSasZX%3D",
	"https://host.example/archive?token=dcTokZX",
	"https://bucket.example/archive#dcCanaryTokenZX_93Kq",
	"https://bucket.example/archive#dc%43anaryTokenZX_93Kq",
	"oci://u:dcPwZX@[2001:db8::1]:5000/org?token=dcQZX#dcFZX",
	"userZX:dcPwZX@registry.example/org",
}

// cleanURLs carry no credential: every print site shows them exactly as given.
var cleanURLs = []string{"dc-rdb", "ghcr.io/devicechain-io", "localhost:5001", "s3://bucket/archive",
	"https://s3.us-east-1.amazonaws.com", "https://[2001:db8::1]:9000/archive"}

func assertNoCredential(t *testing.T, where, out string) {
	t.Helper()
	if strings.Contains(strings.ToLower(out), "zx") {
		t.Errorf("%s printed a credential:\n%s", where, out)
	}
}

// forEachURL runs check once per credential URL, holding its output to "nothing
// recoverable", and once per clean URL, holding its output to "the URL as given".
func forEachURL(t *testing.T, check func(t *testing.T, u string) string) {
	t.Helper()
	for _, u := range credentialURLs {
		t.Run("credential "+u, func(t *testing.T) {
			out := check(t, u)
			if out == "" {
				t.Fatal("the print site printed nothing: the test proves nothing")
			}
			assertNoCredential(t, "the print site", out)
			if !strings.Contains(out, installflow.URLRedaction) {
				t.Errorf("the print site does not show the redaction it made:\n%s", out)
			}
		})
	}
	for _, u := range cleanURLs {
		t.Run("clean "+u, func(t *testing.T) {
			if out := check(t, u); !strings.Contains(out, u) {
				t.Errorf("a URL with no credential is not printed as given (%q):\n%s", u, out)
			}
		})
	}
}

func TestTheRelationalRestoreDescriptionRedactsItsSource(t *testing.T) {
	forEachURL(t, func(t *testing.T, u string) string {
		return strings.Join(describeRelationalRestore(RestorePlan{RdbFrom: u}, clusterArchiveState{}, testNow), "\n")
	})
}

func TestTheIneffectiveRelationalRestoreWarningRedactsItsSource(t *testing.T) {
	forEachURL(t, func(t *testing.T, u string) string {
		return strings.Join(describeRelationalRestore(RestorePlan{RdbFrom: u},
			clusterArchiveState{Exists: true, Path: "dc-rdb-live"}, testNow), "\n")
	})
}

func TestTheRelationalRestorePrinterRedactsItsSource(t *testing.T) {
	forEachURL(t, func(t *testing.T, u string) string {
		var buf bytes.Buffer
		printRelationalRestore(console{out: &buf, err: &buf}, RestorePlan{RdbFrom: u}, clusterArchiveState{}, "")
		return buf.String()
	})
}

// The archive a recovered store OWNS is derived from its source, so it carries the
// source's credential with it: "<source>-restored-<stamp>".
func TestTheDerivedArchivePathIsRedactedWherePrinted(t *testing.T) {
	forEachURL(t, func(t *testing.T, u string) string {
		path := RestoredArchivePath(u, testNow)
		var buf bytes.Buffer
		printArchivePath(console{out: &buf, err: &buf}, "Event archive:", path)
		return buf.String() + eventStoreRestoreNote(RestorePlan{TsdbFrom: u}, path)
	})
}

func TestTheEventStoreRestoreNoteRedactsItsSource(t *testing.T) {
	forEachURL(t, func(t *testing.T, u string) string {
		return eventStoreRestoreNote(RestorePlan{TsdbFrom: u}, "dc-tsdb-acme-restored-20260728T140506Z")
	})
}

func TestTheInstallReportRedactsTheRestoreSourceAndTheArchive(t *testing.T) {
	forEachURL(t, func(t *testing.T, u string) string {
		st := &State{KubeContext: "kind-dc", Restore: RestorePlan{RdbFrom: u},
			Values: map[string]string{"backupServerNameRdb": RestoredArchivePath(u, testNow)}}
		var buf bytes.Buffer
		reportInstall(console{out: &buf, err: &buf}, st, "local")
		out := buf.String()
		if !strings.Contains(out, "Relational recovery:") || !strings.Contains(out, "Relational archive:") {
			t.Fatalf("the report does not reach the lines under test:\n%s", out)
		}
		return out
	})
}

func TestTheImageSourceLabelRedactsTheRegistry(t *testing.T) {
	for _, build := range []bool{false, true} {
		forEachURL(t, func(t *testing.T, u string) string {
			img, err := ResolveImageSource(u, "v1.9.0", build)
			if err != nil {
				t.Fatal(err)
			}
			if img.Registry != u {
				t.Fatalf("the registry itself was changed (%q): only its display may be", img.Registry)
			}
			return img.Label
		})
	}
}

func TestAnUnresolvedImageSourceRefusalRedactsTheRegistry(t *testing.T) {
	forEachURL(t, func(t *testing.T, u string) string {
		err := requireResolvedImages(&State{ImageRegistry: u}, "deploying")
		if err == nil {
			t.Fatal("no refusal")
		}
		return err.Error()
	})
}

func TestTheUpgradeCommandItPrintsRedactsTheRegistry(t *testing.T) {
	forEachURL(t, func(t *testing.T, u string) string {
		return upgradeTargetFlags(&State{ImageRegistry: u, ImageVersion: "v1.9.0"})
	})
}

func TestTheUpgradeImagesLineRedactsTheRegistry(t *testing.T) {
	forEachURL(t, func(t *testing.T, u string) string {
		return upgradeImages(&State{ImageRegistry: u, ImageVersion: "v1.9.0"})
	})
}

func TestTheDryRunLinesNamingTheRegistryRedactIt(t *testing.T) {
	t.Run("install builds the operator", func(t *testing.T) {
		forEachURL(t, func(t *testing.T, u string) string {
			return captureStdout(t, func() {
				st := &State{BuildImages: true, DryRun: true, ImageRegistry: u, ImageVersion: "dev"}
				if err := buildOperatorImageForInstall(context.Background(), st); err != nil {
					t.Error(err)
				}
			})
		})
	})
	t.Run("bootstrap builds the images", func(t *testing.T) {
		forEachURL(t, func(t *testing.T, u string) string {
			return captureStdout(t, func() {
				st := &State{BuildImages: true, DryRun: true, ImageRegistry: u, ImageVersion: "dev"}
				if err := stepLocalRegistry(context.Background(), st); err != nil {
					t.Error(err)
				}
			})
		})
	})
	t.Run("upgrade records the version", func(t *testing.T) {
		forEachURL(t, func(t *testing.T, u string) string {
			return captureStdout(t, func() {
				st := &State{DryRun: true, ImageRegistry: u, ImageVersion: "v1.9.0"}
				if err := recordUpgradedVersion(context.Background(), nil, "acme", st); err != nil {
					t.Error(err)
				}
			})
		})
	})
}

func TestAnAreaChangeRefusalRedactsTheDeclaredRegistry(t *testing.T) {
	forEachURL(t, func(t *testing.T, u string) string {
		inst := &dcv1beta1.Instance{}
		inst.SetUID("0badc0de-0000-4000-8000-000000000001")
		inst.Spec.ImageRegistry, inst.Spec.ImageVersion = u, "v1.9.0"
		err := applyUpgradeDeclaration(&State{}, inst, UpgradeOptions{Instance: "acme",
			EnableAreas: []string{"update-management"}, ImageVersion: "v1.9.1"})
		if err == nil {
			t.Fatal("no refusal")
		}
		return err.Error()
	})
}

// A failed build repeats its command line, and the command line holds the image
// reference built from --registry. Neither ko nor the directory exists here, so each
// fails before it runs anything, deterministically.
func TestAFailedBuildRedactsTheRegistryInItsCommandLine(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	t.Run("ko", func(t *testing.T) {
		forEachURL(t, func(t *testing.T, u string) string {
			err := koBuild(t.Context(), missing, &State{ImageVersion: "dev"}, missing, u+"/operator")
			if err == nil {
				t.Fatal("the build did not fail")
			}
			return err.Error()
		})
	})
	t.Run("docker", func(t *testing.T) {
		forEachURL(t, func(t *testing.T, u string) string {
			err := run(t.Context(), filepath.Join(missing, "docker"), "push", u+"/frontend:dev")
			if err == nil {
				t.Fatal("the command did not fail")
			}
			return err.Error()
		})
	})
}

func TestTheBackupEndpointRefusalRedactsTheEndpoint(t *testing.T) {
	// Every one of these fails the http(s)-with-a-host check, which is what prints it.
	for _, u := range []string{"s3://dcuserZX:dcPwZX@bucket/archive", "ftp://u:dcPwZX@host/x?sig=dcSigZX",
		"https://u:dcPw%zzZX@host/x", "u:dcPwZX@host.example/x"} {
		t.Run(u, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "backup.json")
			body := `{"endpointUrl":"` + u + `","bucketRdb":"a","bucketTsdb":"b","accessKeyId":"AKIDEXAMPLE0001",` +
				`"secretAccessKey":"abcdefghijklmnopqrstuvwxyz0123456789ABCD"}`
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := ParseBackupDestination(p)
			if err == nil || !strings.Contains(err.Error(), "endpointUrl") {
				t.Fatalf("the endpoint was not refused: %v", err)
			}
			assertNoCredential(t, "the refusal", err.Error())
		})
	}
}

func TestAnArchiveMoveRedactsBothEndpoints(t *testing.T) {
	forEachURL(t, func(t *testing.T, u string) string {
		was := describeArchiveMove(ClusterArchive{EndpointURL: u, BucketTsdb: "old"},
			&BackupDestination{EndpointURL: "https://s3.example", BucketTsdb: "new"})
		now := describeArchiveMove(ClusterArchive{EndpointURL: "https://s3.example", BucketTsdb: "old"},
			&BackupDestination{EndpointURL: u, BucketTsdb: "new"})
		return was + "\n" + now
	})
}

func TestTheDestroyArchivePlanRedactsTheEndpoint(t *testing.T) {
	record := func(endpoint string) *InstallRecord {
		rec := &InstallRecord{}
		rec.Settings.DatabaseBackups, rec.Settings.BackupsExternal = true, true
		rec.Outputs.Archive.EndpointURL, rec.Outputs.Archive.BucketTsdb = endpoint, "devicechain-tsdb"
		return rec
	}
	t.Run("external, named from the live store", func(t *testing.T) {
		forEachURL(t, func(t *testing.T, u string) string {
			return planArchiveRemoval("acme", archiveFacts{Record: record(u),
				Live:  clusterArchiveState{Exists: true, Path: "dc-tsdb-acme-1a2b3c4d"},
				Store: liveObjectStore{Bucket: "devicechain-tsdb", Endpoint: u}}).Reason
		})
	})
	t.Run("external, named from the record", func(t *testing.T) {
		forEachURL(t, func(t *testing.T, u string) string {
			return planArchiveRemoval("acme", archiveFacts{Record: record(u), LiveErr: errors.New("x")}).Reason
		})
	})
	t.Run("kept, named from what was recorded", func(t *testing.T) {
		forEachURL(t, func(t *testing.T, u string) string {
			return planArchiveRemoval("acme", archiveFacts{Keep: true, Record: record(u),
				Recorded: &recordedArchive{Instance: "acme", Bucket: "devicechain-tsdb", Prefix: "dc-tsdb-acme/", Endpoint: u}}).Reason
		})
	})
	t.Run("the live store and the record disagree", func(t *testing.T) {
		const inCluster = "http://dc-objectstore.dc-infra.svc:9000"
		disagree := func(t *testing.T, recorded, live string) string {
			rec := record(recorded)
			rec.Settings.BackupsExternal = false
			reason := planArchiveRemoval("acme", archiveFacts{Record: rec,
				Live:  clusterArchiveState{Exists: true, Path: "dc-tsdb-acme-1a2b3c4d"},
				Store: liveObjectStore{Bucket: "devicechain-tsdb", Endpoint: live}}).Reason
			if !strings.Contains(reason, "do not agree") {
				t.Fatalf("the plan does not reach the disagreement: %s", reason)
			}
			return reason
		}
		forEachURL(t, func(t *testing.T, u string) string {
			return disagree(t, inCluster, u) + "\n" + disagree(t, u, inCluster)
		})
	})
}

func TestTheObjectStoreDescriptionRedactsItsEndpoint(t *testing.T) {
	forEachURL(t, func(t *testing.T, u string) string {
		return describeObjectStore(&dcv1beta1.ObjectStoreSpec{Backend: dcv1beta1.ObjectStoreS3,
			S3: &dcv1beta1.S3ObjectStoreSpec{Bucket: "logos", Endpoint: u}})
	})
}

// The broker client quotes a URL it cannot parse whole, userinfo and all.
func TestABrokerDialFailureRedactsTheURL(t *testing.T) {
	for _, u := range []string{"nats://u:dcPwZX@host:x1", "nats://dcTokZX@host:x1?token=dcQZX"} {
		t.Run(u, func(t *testing.T) {
			_, err := dialBrokerCtx(t.Context(), u, config.NatsConfiguration{})
			if err == nil {
				t.Fatal("the dial did not fail")
			}
			assertNoCredential(t, "the dial error", err.Error())
			if !strings.Contains(err.Error(), installflow.URLRedaction) {
				t.Errorf("the error no longer names the URL: %v", err)
			}
		})
	}
}

// 🔴 THE THREE PRESENTERS, END TO END. The real install engine runs a cluster
// stand-in that prints through the restore, archive and registry print sites, and
// the plain output, the --output json stream and the terminal UI's screen are each
// held to "no credential". The URLs are deliberately NOT registered as secrets: the
// structural redaction is the defence under test, not the hub's.
func TestNoPresenterShowsACredentialFromAUserSuppliedURL(t *testing.T) {
	cluster := func(ctx context.Context, _ Provider, _ InstallOptions) error {
		c := out(ctx)
		for _, u := range credentialURLs {
			printRelationalRestore(c, RestorePlan{RdbFrom: u}, clusterArchiveState{}, "")
			printArchivePath(c, "Relational archive:", RestoredArchivePath(u, testNow))
			c.println(eventStoreRestoreNote(RestorePlan{TsdbFrom: u}, RestoredArchivePath(u, testNow)))
			img, err := ResolveImageSource(u, "v1.9.0", false)
			if err != nil {
				return err
			}
			c.println("Images: " + img.Label)
		}
		return nil
	}
	engine := func() *InstallEngine {
		return &InstallEngine{Provider: &fakeProvider{name: "local"}, SkipPreflight: true,
			Options: InstallOptions{AssumeYes: true}, Cluster: cluster}
	}
	req := installflow.Request{Operation: installflow.OperationInstall, AssumeYes: true}
	expectRedactions := func(t *testing.T, where string, b []byte) {
		t.Helper()
		assertNoCredential(t, where, string(b))
		testkit.AssertNoCanary(t, where, b)
		if !bytes.Contains(b, []byte(installflow.URLRedaction)) {
			t.Fatalf("%s carries none of the printed lines: the test proves nothing\n%s", where, b)
		}
	}

	t.Run("plain", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if err := (installplain.Presenter{Stdout: &stdout, Stderr: &stderr}).Run(t.Context(), engine(), req, nil); err != nil {
			t.Fatal(err)
		}
		expectRedactions(t, "the plain output", append(stdout.Bytes(), stderr.Bytes()...))
	})
	t.Run("json", func(t *testing.T) {
		var stream bytes.Buffer
		err := installjson.Presenter{Stdout: &stream, RunID: "urls", Clock: testkit.NewManualClock(testkit.Epoch)}.Run(
			t.Context(), engine(), req, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, derr := installjson.DecodeStream(stream.Bytes()); derr != nil {
			t.Fatal(derr)
		}
		expectRedactions(t, "the event stream", stream.Bytes())
	})
	t.Run("tui", func(t *testing.T) {
		keys, w := io.Pipe()
		t.Cleanup(func() { _ = w.Close() })
		var screen lockedBuffer
		p := installtui.Presenter{Stdin: keys, Stdout: &screen, Stderr: &screen,
			Caps: terminal.Capabilities{Eligible: true, TUI: true, ProgressOnly: true, Level: terminal.L1,
				Width: 200, Height: 120, SizeKnown: true, Layout: terminal.Full}}
		done := make(chan error, 1)
		go func() { done <- p.Run(context.Background(), engine(), req, nil) }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(60 * time.Second):
			t.Fatal("the terminal UI did not finish")
		}
		expectRedactions(t, "the terminal UI", []byte(screen.String()))
	})
}

// The render step is where `dcctl bootstrap` prints its event-store restore note.
func TestTheRenderStepRedactsTheEventStoreRestoreSource(t *testing.T) {
	forEachURL(t, func(t *testing.T, u string) string {
		withArchiveState(t, clusterArchiveState{}, nil)
		st := &State{Instance: "prod", BuildImages: true, DryRun: true, Values: map[string]string{},
			Restore: RestorePlan{TsdbFrom: u}}
		var err error
		out := captureStdout(t, func() { err = stepRenderConfig(t.Context(), st) })
		if err != nil {
			t.Fatalf("stepRenderConfig: %v", err)
		}
		if !strings.Contains(out, "event store recovering from archive") {
			t.Fatalf("the step never printed the restore note:\n%s", out)
		}
		return out
	})
}

// A restored store's archive path is derived from its restore source, so destroy
// prints it, and the prefixes it recorded, through the helper too. The removal itself
// still acts on the path as it is.
func TestDestroyRedactsRestoreDerivedArchivePaths(t *testing.T) {
	const own = "dc-tsdb-acme#dcTokZX-restored-20260101T000000Z" // passes the removal's own-path check
	inCluster := "http://dc-object-store." + infraNamespace + ":9000"
	record := func() *InstallRecord {
		rec := &InstallRecord{}
		rec.Settings.DatabaseBackups = true
		rec.Outputs.Archive.EndpointURL, rec.Outputs.Archive.BucketTsdb = inCluster, "devicechain-tsdb"
		return rec
	}
	live := clusterArchiveState{Exists: true, Path: own}
	plan := planArchiveRemoval("acme", archiveFacts{Record: record(), Live: live,
		Store: liveObjectStore{Bucket: "devicechain-tsdb", Endpoint: inCluster}})
	if plan.Action != archiveRemove || plan.Prefix != own+"/" {
		t.Fatalf("the removal no longer acts on the path as it is: %+v", plan)
	}
	shown := map[string]string{
		"the location":                   plan.location(),
		"a path that is not one segment": archivePathRefusal("acme", "dc-tsdb#dcTokZX/x"),
		"a path that is not this one's":  archivePathRefusal("acme", "dc-tsdb-other#dcTokZX"),
		"kept, named from the live store": planArchiveRemoval("acme", archiveFacts{Keep: true, Record: record(),
			Live: live, Store: liveObjectStore{Bucket: "devicechain-tsdb", Endpoint: inCluster}}).Reason,
		"kept, named from the record": planArchiveRemoval("acme", archiveFacts{Keep: true, Record: record(),
			Recorded: &recordedArchive{Instance: "acme", Bucket: "devicechain-tsdb", Prefix: own + "/", Endpoint: inCluster}}).Reason,
		"the ObjectStore unreadable": planArchiveRemoval("acme", archiveFacts{Record: record(), Live: live,
			StoreErr: errors.New("gone")}).Reason,
		"the stores disagree": planArchiveRemoval("acme", archiveFacts{Record: record(), Live: live,
			Store: liveObjectStore{Bucket: "devicechain-tsdb", Endpoint: "http://minio.elsewhere:9000"}}).Reason,
	}
	for what, text := range shown {
		if !strings.Contains(text, "dc-tsdb") {
			t.Errorf("%s does not name the archive, so this proves nothing: %q", what, text)
		}
		assertNoCredential(t, what, text)
	}
}

// The in-cluster endpoint check quotes the endpoint it refuses.
func TestTheInClusterEndpointRefusalsRedactTheEndpoint(t *testing.T) {
	for _, u := range []string{"ftp://u:dcPwZX@dc-object-store.dc-infra:9000",
		"https://u:dcPwZX@s3.example.com", "http://u:dcPwZX@dc-object-store." + infraNamespace + ":99999999999999999999"} {
		_, _, err := parseInClusterEndpoint(u)
		if err == nil {
			t.Fatalf("%s was accepted", u)
		}
		assertNoCredential(t, "the refusal", err.Error())
	}
}

// 🔴 DEFENCE IN DEPTH IN THE ROOTS THEMSELVES: the outputs and the plan attribute that
// carry a restore source or a user-supplied endpoint are sensitive, so OpenTofu prints
// them as (sensitive value) whoever drives it. dcctl reads them with `output -json`,
// which still returns them.
func TestTheRootsMarkRestoreSourcesAndEndpointsSensitive(t *testing.T) {
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "opentofu", rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	block := func(src, header string) string {
		i := strings.Index(src, header)
		if i < 0 {
			t.Fatalf("%q is gone", header)
		}
		end := strings.Index(src[i:], "\n}\n")
		return src[i : i+end]
	}
	for _, tc := range []struct{ file, header, want string }{
		{"cluster/outputs.tf", `output "backup_endpoint_url" {`, "sensitive = true"},
		{"cluster/outputs.tf", `output "database_restored_from" {`, "sensitive = true"},
		{"instance/outputs.tf", `output "database_restored_from" {`, "sensitive = true"},
		{"cluster/main.tf", `resource "terraform_data" "restore_guard" {`, "input = sensitive(var.restore_rdb_from)"},
		{"instance/main.tf", `resource "terraform_data" "restore_guard" {`, "input = sensitive(var.restore_tsdb_from)"},
	} {
		if !strings.Contains(block(read(tc.file), tc.header), tc.want) {
			t.Errorf("%s: %s does not say %q", tc.file, tc.header, tc.want)
		}
	}
}
