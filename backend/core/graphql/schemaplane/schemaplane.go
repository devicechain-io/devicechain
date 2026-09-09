// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package schemaplane classifies the GraphQL schema artifacts a functional area
// serves: which file is served at which mount, under which kind of token.
//
// 🔴 WHY THIS EXISTS RATHER THAN A FILENAME SUBSTRING. Every tool that reads a
// services tree used to decide a schema's auth plane with
// `strings.Contains(base, "admin")`, and then CONCATENATE everything that did not
// match into one SDL. Two things go wrong with that, and only the second is
// visible:
//
//   - user-management's settings schema is an identity-token surface served at
//     /settings/graphql, and its filename contains no "admin", so it was folded
//     into the tenant plane.
//   - the fold then joined two files that each declare `type Query` and
//     `type Mutation`. The pinned graphql-go fork accepts a duplicate root type
//     without an error and keeps the LAST one, so the concatenated tenant "schema"
//     served settings and nothing else — no ping, no me, no login. Every consumer's
//     `if sdl.Len() == 0` floor stayed quiet, because the SDL was not empty. It was
//     wrong.
//
// So the unit here is not a plane but a MOUNT: one file, one endpoint, parsed on
// its own, exactly as the services do it (each schema is embedded into its own
// variable and handed to its own MustParseSchema — there is no concatenation in
// production). Dir refuses a directory where two files claim one mount, which makes
// that fold unrepresentable rather than merely unlikely.
//
// The other half is the extension, and it splits into two jobs that must not be
// one function:
//
//   - Lint is a rule about THIS repository's tree. A schema artifact here is
//     `.graphql` and nothing else, because that is the only extension the SPDX
//     header gate can see: addlicense has no handler for `.gql`, so a `.gql` schema
//     is skipped in silence and its header survives only for as long as somebody
//     keeps typing it. Lint refuses such a file by name rather than ignoring it —
//     an ignored one is how the split above arose.
//   - Dir, Classify, At and SDLAt are a SCANNER, and what they are pointed at is
//     usually not this tree. apiprobe runs them over a CHECKED-OUT RELEASE — the
//     upgrade drill seeds the previous release and then upgrades it — and an older
//     tree spells its schemas the way that release spelled them. So the scanner
//     accepts every schema-shaped extension and classifies on the base name with
//     the extension normalized. A released tree is immutable; refusing its spelling
//     does not rename anything, it only makes the tree unreadable.
//
// The strictness lives entirely in Lint rather than in a mode flag on Dir, because
// a scanner with a mode is eventually run in the wrong one, and the failure then
// reads as a schema problem rather than as the configuration mistake it is.
//
// This mirrors FILENAME_CONVENTIONS in docs/scripts/schemas.manifest.mjs, which is
// the same table for the docs publisher.
package schemaplane

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Ext is the extension a GraphQL schema artifact carries in THIS repository, and
// the one every classification normalizes to.
const Ext = ".graphql"

// altExts are the other extensions a GraphQL schema artifact is written under. Lint
// refuses them in this tree; the scanner reads them, because a release that already
// shipped under one of them cannot be renamed after the fact. They are listed
// rather than inferred so that a file under an unrelated extension stays a file
// this package ignores, not a schema it guesses at.
var altExts = []string{".gql", ".graphqls", ".gqls", ".sdl"}

// Plane names the kind of token an endpoint authenticates with. It is an attribute
// of a mount, not a way to address one — two mounts share the identity plane.
type Plane string

const (
	// PlaneTenant is the ordinary application plane: a tenant access token.
	PlaneTenant Plane = "tenant"
	// PlaneIdentity is instance-scoped administration: the identity token login
	// returns BEFORE a tenant is selected. A tenant access token is rejected.
	PlaneIdentity Plane = "identity"
)

// The mounts a DeviceChain service registers. Each is served by exactly one schema
// file, parsed on its own.
const (
	MountTenant   = "/graphql"
	MountAdmin    = "/admin/graphql"
	MountSettings = "/settings/graphql"
)

// convention maps a schema artifact's filename to the endpoint that serves it.
// Verified against the servers that register them: /graphql in the shared core,
// /admin/graphql and /settings/graphql in user-management and ai-inference.
var conventions = []struct {
	File  string
	Mount string
	Plane Plane
}{
	{"schema" + Ext, MountTenant, PlaneTenant},
	{"admin_schema" + Ext, MountAdmin, PlaneIdentity},
	{"settings_schema" + Ext, MountSettings, PlaneIdentity},
}

// Schema is one classified schema artifact.
type Schema struct {
	// Path is the file, as it was found.
	Path string
	// Mount is the endpoint the owning service serves this file at.
	Mount string
	// Plane is the kind of token that mount authenticates with.
	Plane Plane
}

// isSchemaShaped reports whether an extension is one a GraphQL schema artifact is
// written under — Ext or any of altExts. The comparison is case-insensitive.
func isSchemaShaped(ext string) bool {
	ext = strings.ToLower(ext)
	if ext == Ext {
		return true
	}
	for _, alt := range altExts {
		if ext == alt {
			return true
		}
	}
	return false
}

// canonicalName is filename's base with a schema-shaped extension normalized to
// Ext, so that one convention table answers for every spelling a release used. The
// bool reports whether the file is schema-shaped at all; a file that is not keeps
// its name and is left for the caller to reject or ignore.
func canonicalName(filename string) (string, bool) {
	base := filepath.Base(filename)
	ext := filepath.Ext(base)
	if !isSchemaShaped(ext) {
		return base, false
	}
	return strings.TrimSuffix(base, ext) + Ext, true
}

// Classify resolves a schema artifact's filename to its mount and plane, reading
// through the extension: schema.gql and schema.graphql are the same artifact under
// two spellings and classify identically, because a tree checked out from an older
// release spells them the way that release did.
//
// An unrecognised name is an error. Guessing is what this package exists to stop:
// a schema whose plane is guessed is either offered to a principal that can never
// authorize it, or hidden from the one that can. The extension is normalized; the
// NAME never is.
func Classify(filename string) (Schema, error) {
	base, _ := canonicalName(filename)
	for _, c := range conventions {
		if base == c.File {
			return Schema{Path: filename, Mount: c.Mount, Plane: c.Plane}, nil
		}
	}
	names := make([]string, 0, len(conventions))
	for _, c := range conventions {
		names = append(names, c.File)
	}
	return Schema{}, fmt.Errorf(
		"%s: unrecognized GraphQL schema filename; the mount and the auth plane are both "+
			"derived from it, so it must be one of %s — rename the file, or add the new "+
			"convention to schemaplane.conventions and to FILENAME_CONVENTIONS in "+
			"docs/scripts/schemas.manifest.mjs",
		filename, strings.Join(names, ", "))
}

// Lint holds one of THIS repository's graphql directories to the naming rule: every
// schema artifact in it is named Ext, and named something a convention recognises.
//
// It is the strict half of the pair. Dir reads any release's spelling because it has
// to; Lint refuses ours, because here the file can still be renamed and there is a
// reason to: addlicense has no handler for the alternatives, so a schema under one
// of them is skipped by the SPDX header gate in silence, and its header lasts only
// as long as somebody keeps typing it by hand.
//
// Point it at a directory in this tree — not at a checked-out release, which is not
// ours to rename.
func Lint(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read schema directory %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if !isSchemaShaped(ext) || ext == Ext {
			continue
		}
		return fmt.Errorf(
			"%s: a GraphQL schema artifact must be named %q, not %q. Nothing skips this "+
				"file quietly: the SPDX header gate has no handler for %q and would stop "+
				"checking it, and a consumer globbing %q would stop reading it",
			filepath.Join(dir, name), Ext, ext, ext, Ext)
	}
	// The rest of the naming rule — a recognised name, one file per mount — is the
	// same in both halves, so it is asserted by running the scanner rather than
	// restated here where the two could drift apart.
	_, err = Dir(dir)
	return err
}

// Dir classifies every schema artifact in one area's graphql directory, sorted by
// path. A directory that holds no schema artifact yields an empty slice and no
// error — not every directory named graphql serves one.
//
// 🔴 THIS IS THE SCANNER, NOT THE LINT. It is pointed at trees this repository does
// not control — apiprobe reads a checked-out release — so it accepts every
// schema-shaped extension and classifies on the normalized name. Refusing an older
// release's spelling would not rename a single file in it; it would only make the
// tree unreadable, which is how the upgrade drill stopped being able to seed its
// baseline. Use Lint for the rule about our own tree.
//
// It fails on a schema artifact no convention names, and on two files claiming one
// mount.
func Dir(dir string) ([]Schema, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read schema directory %s: %w", dir, err)
	}

	var out []Schema
	byMount := map[string]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !isSchemaShaped(filepath.Ext(name)) {
			continue
		}

		s, cerr := Classify(filepath.Join(dir, name))
		if cerr != nil {
			return nil, cerr
		}
		if prev, dup := byMount[s.Mount]; dup {
			return nil, fmt.Errorf(
				"%s and %s both claim mount %s. Each mount is served by ONE schema parsed on its "+
					"own; two files here would have to be concatenated, and graphql-go keeps the "+
					"LAST of two duplicate root types without reporting an error — so the endpoint "+
					"would serve one file's fields and silently drop the other's",
				prev, s.Path, s.Mount)
		}
		byMount[s.Mount] = s.Path
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// At returns the schema an area serves at one mount, reading dir. The bool reports
// whether the area serves that mount at all; an area with no such schema is a
// normal, expected answer and not an error.
func At(dir, mount string) (Schema, bool, error) {
	all, err := Dir(dir)
	if err != nil {
		return Schema{}, false, err
	}
	for _, s := range all {
		if s.Mount == mount {
			return s, true, nil
		}
	}
	return Schema{}, false, nil
}

// SDLAt returns the schema TEXT an area serves at one mount.
//
// It returns the text of a single file, never a concatenation — see the package
// comment. An empty file is an error here rather than an empty string a caller has
// to remember to check, because a parser handed nothing validates everything.
func SDLAt(dir, mount string) (string, bool, error) {
	s, ok, err := At(dir, mount)
	if err != nil || !ok {
		return "", ok, err
	}
	body, err := os.ReadFile(s.Path) //nolint:gosec // a path this package classified itself
	if err != nil {
		return "", false, fmt.Errorf("read %s: %w", s.Path, err)
	}
	if strings.TrimSpace(string(body)) == "" {
		return "", false, fmt.Errorf("%s is empty; a schema that declares nothing would accept every document", s.Path)
	}
	return string(body), true, nil
}
