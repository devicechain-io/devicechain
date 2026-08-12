// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
)

// pointConfigDirAt redirects the package-level mount path for one test. The
// resolver reads a fixed location because that is where the chart mounts the
// volume, so the seam for a test is the variable itself.
func pointConfigDirAt(t *testing.T, dir string) {
	t.Helper()
	prev := functionalAreaConfigDir
	functionalAreaConfigDir = dir
	t.Cleanup(func() { functionalAreaConfigDir = prev })
}

func writeAreas(t *testing.T, areas ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, a := range areas {
		if err := os.WriteFile(filepath.Join(dir, a), []byte("{}"), 0o644); err != nil {
			t.Fatalf("writing %q: %v", a, err)
		}
	}
	return dir
}

// 🔴 The security-relevant test. The tenant GraphQL handler admits requests with
// no token (login/refresh must run before one exists) and this resolver touches
// no tenant-scoped table, so the fail-closed DB callback never fires for it.
// Without the explicit check in the resolver, the instance's area topology is
// readable by anyone who can reach the URL. Measured against a live instance
// before the check existed: an unauthenticated POST answered 200.
func TestFunctionalAreasRefusesAnUnauthenticatedTenantCaller(t *testing.T) {
	pointConfigDirAt(t, writeAreas(t, "user-management"))

	_, err := (&SchemaResolver{}).FunctionalAreas(context.Background())
	if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("got %v, want ErrUnauthenticated", err)
	}
}

// The counterweight to the test above: refusing the anonymous caller is only
// correct while an authenticated one is still served. Without this, deleting the
// resolver body entirely would leave the suite green.
func TestFunctionalAreasServesAnAuthenticatedTenantCaller(t *testing.T) {
	pointConfigDirAt(t, writeAreas(t, "device-management", "user-management"))

	ctx := auth.WithClaims(context.Background(), &auth.Claims{Username: "someone"})
	got, err := (&SchemaResolver{}).FunctionalAreas(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []string{"device-management", "user-management"}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// The admin plane carries no claims check of its own because NewAdminHttpHandler
// validates an identity token before any resolver runs (verified live: that
// endpoint answers 401 unauthenticated, where the tenant plane answers 200). This
// pins the resolver's actual contract — it serves the same set — so that the
// asymmetry with the tenant plane stays a deliberate decision rather than
// something a later reader "fixes" in either direction.
func TestAdminFunctionalAreasServesTheSameSet(t *testing.T) {
	pointConfigDirAt(t, writeAreas(t, "ai-inference", "user-management"))

	got, err := (&AdminResolver{}).FunctionalAreas(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []string{"ai-inference", "user-management"}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// An unreadable mount must surface as an error, not as an empty set. "No areas
// are deployed" is a claim the console would act on by hiding everything, and it
// is never true — user-management is answering the question.
func TestFunctionalAreasErrorsWhenTheMountIsAbsent(t *testing.T) {
	pointConfigDirAt(t, filepath.Join(t.TempDir(), "absent"))

	ctx := auth.WithClaims(context.Background(), &auth.Claims{Username: "someone"})
	if _, err := (&SchemaResolver{}).FunctionalAreas(ctx); err == nil {
		t.Fatal("expected an error when the config mount is absent, got nil")
	}
}
