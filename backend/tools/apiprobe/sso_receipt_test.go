// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// The admin-plane rows through the REAL verify subcommand, not verifyAdminRows alone:
// testing the function says nothing about whether verify CALLS it. A verify that read
// the tenant rows and stepped over the admin ones would pass every test of the function
// and report green over a connection it never looked at.
func TestVerifyReadsTheReceiptsAdminRowsThroughTheRealCommand(t *testing.T) {
	const object = `{"token":"apiprobe-idp-apiprobe","enabled":false}`
	row := Recorded{
		Name: ssoRowName, Area: "user-management", Token: ssoConnectionToken("apiprobe"),
		Object: json.RawMessage(object), Fields: idpConnectionFields,
	}
	serve := func(answer string, reads *atomic.Int32) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			reads.Add(1)
			_, _ = fmt.Fprintf(w, `{"data":{"idpConnection":%s}}`, answer)
		}
	}

	t.Run("unchanged passes and says how many it read", func(t *testing.T) {
		t.Setenv(adminPasswordEnv, "pw")
		var reads atomic.Int32
		f := newFakeInstance(t)
		f.superuser, f.adminRows, f.admin = true, []Recorded{row}, serve(object, &reads)
		if err := f.verify(); err != nil {
			t.Fatalf("verify: %v (exit %d)\n%s", err, codeOf(err), f.out.String())
		}
		if reads.Load() != 1 {
			t.Fatalf("the admin plane was read %d times, want 1", reads.Load())
		}
		if !strings.Contains(f.out.String(), "1 of 1 recorded admin-plane rows read back unchanged") {
			t.Fatalf("the report does not state the admin-plane rows it checked:\n%s", f.out.String())
		}
	})
	t.Run("gone is MISSING", func(t *testing.T) {
		t.Setenv(adminPasswordEnv, "pw")
		var reads atomic.Int32
		f := newFakeInstance(t)
		f.superuser, f.adminRows, f.admin = true, []Recorded{row}, serve("null", &reads)
		assertCode(t, f.verify(), exitMissing)
	})
	t.Run("changed is MISMATCH", func(t *testing.T) {
		t.Setenv(adminPasswordEnv, "pw")
		var reads atomic.Int32
		f := newFakeInstance(t)
		f.superuser, f.adminRows = true, []Recorded{row}
		f.admin = serve(`{"token":"apiprobe-idp-apiprobe","enabled":true}`, &reads)
		assertCode(t, f.verify(), exitMismatch)
	})
	t.Run("no superuser password is inconclusive, not a pass", func(t *testing.T) {
		t.Setenv(adminPasswordEnv, "")
		var reads atomic.Int32
		f := newFakeInstance(t)
		f.superuser, f.adminRows, f.admin = true, []Recorded{row}, serve(object, &reads)
		assertCode(t, f.verify(), exitSetup)
		if reads.Load() != 0 {
			t.Fatal("read the admin plane with no superuser signed in")
		}
	})
	t.Run("a sign-in that is not the superuser is inconclusive, not a pass", func(t *testing.T) {
		t.Setenv(adminPasswordEnv, "pw")
		var reads atomic.Int32
		f := newFakeInstance(t)
		f.adminRows, f.admin = []Recorded{row}, serve(object, &reads)
		assertCode(t, f.verify(), exitSetup)
	})
	t.Run("a receipt with no admin rows needs no password", func(t *testing.T) {
		t.Setenv(adminPasswordEnv, "")
		f := newFakeInstance(t)
		if err := f.verify(); err != nil {
			t.Fatalf("verify of a receipt with no admin rows: %v (exit %d)", err, codeOf(err))
		}
		if strings.Contains(f.out.String(), "admin-plane") {
			t.Fatalf("the report speaks of admin-plane rows the receipt does not have:\n%s", f.out.String())
		}
	})
}

// What runSeed calls for the admin plane: the row goes on the receipt when it is written,
// the skip goes on the skipped list when it is not, and the superuser is signed in only
// when there is something to write.
func TestSeedAdminRowsRecordsWhatItWroteOrWhatItSkipped(t *testing.T) {
	ctx := context.Background()
	t.Run("no provider: skipped by name, no superuser", func(t *testing.T) {
		var out bytes.Buffer
		rows, skipped, err := seedAdminRows(ctx, ssoOptions{}, nil, "apiprobe", "http://x/admin",
			func(context.Context) (querier, error) {
				t.Fatal("signed in as the superuser with nothing to write")
				return nil, nil
			}, &out)
		if err != nil || len(rows) != 0 || len(skipped) != 1 || skipped[0] != ssoRowName {
			t.Fatalf("rows=%v skipped=%v err=%v", rows, skipped, err)
		}
		if !strings.Contains(out.String(), "--idp-issuer") {
			t.Fatalf("the skip does not say why:\n%s", out.String())
		}
	})
	t.Run("a provider: the row is on the receipt, nothing skipped", func(t *testing.T) {
		f := &fakeAdmin{}
		rows, skipped, err := seedAdminRows(ctx, ssoOptions{issuer: "https://idp.invalid"}, nil, "apiprobe",
			"http://x/admin", func(context.Context) (querier, error) { return f, nil }, &bytes.Buffer{})
		if err != nil || len(skipped) != 0 || len(rows) != 1 ||
			rows[0].Name != ssoRowName || rows[0].Token != ssoConnectionToken("apiprobe") {
			t.Fatalf("rows=%+v skipped=%v err=%v", rows, skipped, err)
		}
	})
	t.Run("no superuser session fails the seed", func(t *testing.T) {
		_, _, err := seedAdminRows(ctx, ssoOptions{issuer: "https://idp.invalid"}, nil, "apiprobe",
			"http://x/admin", func(context.Context) (querier, error) {
				return nil, failWith(exitSetup, "no superuser password")
			}, &bytes.Buffer{})
		if err == nil || codeOf(err) != exitSetup {
			t.Fatalf("err=%v", err)
		}
	})
}

// 🔴 THE ANSWER THE RIG PARSES, as printed. hack/upgrade-rig.sh stands the stub provider
// up, and passes --idp-issuer to the baseline seed, only on the exact word "supported".
// An answer stuck on "unsupported" would skip the baseline's connection BY NAME on every
// drill, forever, and every drill would stay green — so the current tree, which can
// create a connection, must get "supported", and a tree that cannot must not.
func TestSSOBaselinePrintsTheWordTheRigParses(t *testing.T) {
	var out bytes.Buffer
	if err := runSSOBaselineTo([]string{"--baseline-schemas", filepath.Join("..", "..", "services")}, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "supported\n" {
		t.Fatalf("the current tree answered %q, want exactly \"supported\n\"", out.String())
	}

	dir := t.TempDir()
	graphqlDir := filepath.Join(dir, "user-management", "graphql")
	if err := os.MkdirAll(graphqlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(graphqlDir, "schema.graphql"), []byte("type Query { ping: String }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runSSOBaselineTo([]string{"--baseline-schemas", dir}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "unsupported: ") {
		t.Fatalf("a tree with no admin schema answered %q", out.String())
	}

	// No tree is a setup failure, never an answer the rig could read as either word.
	out.Reset()
	assertCode(t, runSSOBaselineTo(nil, &out), exitSetup)
	if out.Len() != 0 {
		t.Fatalf("a refused question still printed an answer: %q", out.String())
	}
}
