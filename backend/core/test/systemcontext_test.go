// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// scanSystemContextFixture writes the named sources into a temporary tree and returns
// the call sites the scanner reports, as "function" strings sorted for comparison.
//
// Fixtures are how this scanner is shown to FIRE. The repository test next door can
// only ever show it staying quiet, and a scanner that reports clean because it is
// broken produces exactly that same silence.
func scanSystemContextFixture(t *testing.T, files map[string]string) []string {
	t.Helper()
	dir := t.TempDir()
	for name, src := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("creating %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}
	found, _, err := systemContextSitesUnder(dir)
	if err != nil {
		t.Fatalf("scanning the fixture: %v", err)
	}
	var out []string
	for _, s := range found {
		out = append(out, s.Function)
	}
	sort.Strings(out)
	return out
}

func assertSites(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("scanner reported %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("scanner reported %v, want %v", got, want)
		}
	}
}

// The plain shape: the core package imported under its own name.
func TestAPlainSystemContextCallIsReported(t *testing.T) {
	got := scanSystemContextFixture(t, map[string]string{"a.go": `package svc

import (
	"context"

	"github.com/devicechain-io/dc-microservice/core"
)

func readEverything(ctx context.Context) context.Context {
	return core.WithSystemContext(ctx)
}
`})
	assertSites(t, got, "readEverything")
}

// The tree spells this package both "core" and "dccore". Matching on the identifier
// would find one of the two, and would keep finding only one as new aliases appear.
func TestAnAliasedImportIsReported(t *testing.T) {
	got := scanSystemContextFixture(t, map[string]string{"a.go": `package svc

import (
	"context"

	dccore "github.com/devicechain-io/dc-microservice/core"
)

func readEverything(ctx context.Context) context.Context {
	return dccore.WithSystemContext(ctx)
}
`})
	assertSites(t, got, "readEverything")
}

// 🔴 THE LOAD-BEARING NEGATIVE. Resolving through the import path rather than the
// identifier is only worth the extra code if an unrelated package spelling itself
// "core" is NOT reported — otherwise the path lookup is decoration over a name match.
func TestASameNamedFunctionOnAnUnrelatedPackageIsNotReported(t *testing.T) {
	got := scanSystemContextFixture(t, map[string]string{"a.go": `package svc

import (
	"context"

	core "example.com/somewhere/else/core"
)

func readEverything(ctx context.Context) context.Context {
	return core.WithSystemContext(ctx)
}
`})
	assertSites(t, got)
}

// 🔴 THE NEGATIVE THAT ACTUALLY REACHES THE DISCRIMINATION. The two fixtures above
// look like they pin the import-path lookup and do not: neither file imports the real
// core package, so the scanner returns early before it ever compares an identifier, and
// a mutant that dropped the comparison entirely passed both of them. This file imports
// the real package AND calls something of the same name on another one, which is the
// only shape where the comparison is what decides. Exactly one site is owed.
func TestASameNamedCallBesideARealOneIsNotReported(t *testing.T) {
	got := scanSystemContextFixture(t, map[string]string{"a.go": `package svc

import (
	"context"

	"github.com/devicechain-io/dc-microservice/core"
	other "example.com/somewhere/else/core"
)

func readEverything(ctx context.Context) context.Context {
	_ = other.WithSystemContext(ctx)
	return core.WithSystemContext(ctx)
}
`})
	assertSites(t, got, "readEverything")
}

// A method of the same name on a value is not this bypass either.
func TestAMethodOfTheSameNameIsNotReported(t *testing.T) {
	got := scanSystemContextFixture(t, map[string]string{"a.go": `package svc

import "context"

type session struct{}

func (session) WithSystemContext(ctx context.Context) context.Context { return ctx }

func readEverything(ctx context.Context, s session) context.Context {
	return s.WithSystemContext(ctx)
}
`})
	assertSites(t, got)
}

// Inside the declaring package the call is unqualified, and a bypass added there would
// be the most consequential one in the tree.
func TestAnUnqualifiedCallInsideTheDeclaringPackageIsReported(t *testing.T) {
	got := scanSystemContextFixture(t, map[string]string{"core/system.go": `package core

import "context"

type systemContextKey struct{}

func WithSystemContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, systemContextKey{}, true)
}

func bootstrapRead(ctx context.Context) context.Context {
	return WithSystemContext(ctx)
}
`})
	assertSites(t, got, "bootstrapRead")
}

// The counterweight to the test above, in the shape that reaches the signature check.
// A same-named function taking and returning something else is a different function,
// and the arity guards alone do not see that: this one has one parameter and one result
// just like the real thing, so only the TYPES tell them apart. Written separately from
// the no-argument case below because a mutant that deleted the type comparison passed
// that one — its fake has no parameters, so an arity guard rejected it first.
func TestAnUnqualifiedCallToADifferentlyTypedFunctionOfTheSameNameIsNotReported(t *testing.T) {
	got := scanSystemContextFixture(t, map[string]string{"other/a.go": `package core

func WithSystemContext(label string) string { return label }

func readEverything() string {
	return WithSystemContext("x")
}
`})
	assertSites(t, got)
}

// The counterweight to the test above: "package core" is not a licence to claim every
// unqualified call of that name. Only the package that DECLARES it owns the identifier.
func TestAnUnqualifiedCallInAPackageThatDoesNotDeclareItIsNotReported(t *testing.T) {
	got := scanSystemContextFixture(t, map[string]string{"other/a.go": `package core

import "context"

func WithSystemContext() string { return "" }

func readEverything(ctx context.Context) string {
	_ = ctx
	return WithSystemContext()
}
`})
	assertSites(t, got)
}

// A bypass installed inside a closure is attributed to the function a reader opens,
// not dropped for having no FuncDecl of its own.
func TestACallInsideAFuncLiteralIsAttributedToItsEnclosingFunction(t *testing.T) {
	got := scanSystemContextFixture(t, map[string]string{"a.go": `package svc

import (
	"context"

	"github.com/devicechain-io/dc-microservice/core"
)

func sweep(ctx context.Context, run func(func() error)) {
	run(func() error {
		_ = core.WithSystemContext(ctx)
		return nil
	})
}
`})
	assertSites(t, got, "sweep")
}

// A package-level initializer has no enclosing function, and a bypass parked there is
// harder to notice than one in a function rather than easier — so it is reported under
// a name that cannot be mistaken for a real one.
func TestAPackageLevelCallIsReported(t *testing.T) {
	got := scanSystemContextFixture(t, map[string]string{"a.go": `package svc

import (
	"context"

	"github.com/devicechain-io/dc-microservice/core"
)

var backgroundSystemCtx = core.WithSystemContext(context.Background())
`})
	assertSites(t, got, packageLevelSite)
}

// Two types in one file carrying a method of the same name are two different bypasses,
// and a ledger that spelled them both "sys" could not tell one going missing from the
// other being added.
func TestMethodsAreQualifiedByTheirReceiverType(t *testing.T) {
	got := scanSystemContextFixture(t, map[string]string{"a.go": `package svc

import (
	"context"

	"github.com/devicechain-io/dc-microservice/core"
)

type readStore struct{}
type writeStore struct{}

func (s *readStore) sys(ctx context.Context) context.Context {
	return core.WithSystemContext(ctx)
}

func (s writeStore) sys(ctx context.Context) context.Context {
	return core.WithSystemContext(ctx)
}
`})
	assertSites(t, got, "readStore.sys", "writeStore.sys")
}

// Tests build system contexts to drive the callbacks they are testing. Those are not
// bypasses shipped to anyone, and listing them would bury the ones that are.
func TestCallsInTestFilesAreNotReported(t *testing.T) {
	got := scanSystemContextFixture(t, map[string]string{
		"a.go": `package svc

func Nothing() {}
`,
		"a_test.go": `package svc

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
)

func TestSomething(t *testing.T) {
	_ = core.WithSystemContext(context.Background())
}
`})
	assertSites(t, got)
}

// A blank import binds no identifier, so nothing in the file can be calling through it.
func TestABlankImportBindsNoName(t *testing.T) {
	got := scanSystemContextFixture(t, map[string]string{"a.go": `package svc

import (
	"context"

	_ "github.com/devicechain-io/dc-microservice/core"
)

type core struct{}

func (core) WithSystemContext(ctx context.Context) context.Context { return ctx }

func readEverything(ctx context.Context, c core) context.Context {
	return c.WithSystemContext(ctx)
}
`})
	assertSites(t, got)
}
