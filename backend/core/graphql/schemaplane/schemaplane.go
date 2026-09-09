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
// The other half is the extension. A schema artifact is `.graphql` and nothing
// else, because that is the only extension the SPDX header gate can see: addlicense
// has no handler for `.gql`, so a `.gql` schema is skipped in silence and its
// header is present only for as long as somebody keeps typing it. Dir therefore
// treats a schema-shaped file under any other extension as an error naming the
// file, not as a file to ignore — an ignored one is how the split arose.
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

// Ext is the one extension a GraphQL schema artifact may carry.
const Ext = ".graphql"

// refusedExts are the schema-shaped extensions that are NOT Ext. A file carrying
// one is an error rather than a skip: silently ignoring it is precisely how a
// service's schemas became invisible to a *.graphql consumer.
var refusedExts = []string{".gql", ".graphqls", ".gqls", ".sdl"}

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

// Classify resolves a schema artifact's filename to its mount and plane.
//
// An unrecognised name is an error. Guessing is what this package exists to stop:
// a schema whose plane is guessed is either offered to a principal that can never
// authorize it, or hidden from the one that can.
func Classify(filename string) (Schema, error) {
	base := filepath.Base(filename)
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

// Dir classifies every schema artifact in one area's graphql directory, sorted by
// path. A directory that holds no schema artifact yields an empty slice and no
// error — not every directory named graphql serves one.
//
// It fails on a schema-shaped file under a refused extension, on a .graphql file no
// convention names, and on two files claiming one mount.
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
		ext := strings.ToLower(filepath.Ext(name))

		for _, bad := range refusedExts {
			if ext == bad {
				return nil, fmt.Errorf(
					"%s: a GraphQL schema artifact must be named %q, not %q. Nothing skips this "+
						"file quietly: the SPDX header gate has no handler for %q and would stop "+
						"checking it, and a consumer globbing %q would stop reading it",
					filepath.Join(dir, name), Ext, ext, ext, Ext)
			}
		}
		if ext != Ext {
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
