// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package clitree compares the dcctl command tree of two builds: every command path,
// its aliases, and every flag it accepts (name, shorthand, type, default, hidden,
// deprecated).
//
// The tree is exported by a test injected into each build's own cmd package (see
// package export), so the baseline is the baseline's REAL cobra tree and not a
// description of it that could have drifted.
package clitree

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/devicechain-io/dc-docsgql/apicompat/decl"
)

// Surface is the name dcctl changes are declared under.
const Surface = "dcctl"

// Flag is one flag a command accepts. A persistent flag a parent declares is listed on
// every command beneath it, because what a user can type is what the command accepts,
// not where the flag was declared: moving a flag from a command to its parent breaks
// nobody and must not read as a removal.
type Flag struct {
	Name       string `json:"name"`
	Shorthand  string `json:"shorthand,omitempty"`
	Type       string `json:"type"`
	Default    string `json:"default"`
	Hidden     bool   `json:"hidden,omitempty"`
	Deprecated string `json:"deprecated,omitempty"`
	// Required is cobra's MarkFlagRequired.
	Required bool `json:"required,omitempty"`
	// Groups are the flag-group constraints the flag is in ("mutually_exclusive:a b",
	// "one_required:a b", "required_if_others_set:a b"): each one can refuse an
	// invocation the baseline accepted.
	Groups []string `json:"groups,omitempty"`
}

// Command is one node of the tree. Path is the space-separated command path below the
// root, "" for the root itself.
type Command struct {
	Path string `json:"path"`
	// Use is the usage line. Positional arguments are not introspectable (cobra's Args
	// is a function), so a changed usage line is the only sign of them and is reported.
	Use        string   `json:"use"`
	Aliases    []string `json:"aliases,omitempty"`
	Hidden     bool     `json:"hidden,omitempty"`
	Deprecated string   `json:"deprecated,omitempty"`
	Flags      []Flag   `json:"flags"`
}

// Tree is a whole command tree.
type Tree struct {
	Commands []Command `json:"commands"`
}

// Read loads an exported tree and refuses an empty one: a comparison against a tree
// that holds nothing would report every command as new and pass.
func Read(path string) (Tree, error) {
	var t Tree
	b, err := os.ReadFile(path)
	if err != nil {
		return t, err
	}
	if err := json.Unmarshal(b, &t); err != nil {
		return t, fmt.Errorf("%s: %w", path, err)
	}
	if len(t.Commands) == 0 {
		return t, fmt.Errorf("%s holds no commands; the export did not capture the tree", path)
	}
	root := false
	for _, c := range t.Commands {
		if c.Path == "" {
			root = true
		}
	}
	if !root {
		return t, fmt.Errorf("%s has no root command; the export did not walk from the root", path)
	}
	return t, nil
}

func coord(path string) string {
	if path == "" {
		return Surface
	}
	return strings.ReplaceAll(path, " ", ".")
}

func flagCoord(path, name string) string { return coord(path) + ":--" + name }

// Kinds maps every change kind to its severity; the declarations parser takes it.
var Kinds = map[string]decl.Severity{
	"command-removed":        decl.Breaking,
	"alias-removed":          decl.Breaking,
	"flag-removed":           decl.Breaking,
	"flag-type-changed":      decl.Breaking,
	"flag-shorthand-changed": decl.Breaking,
	"flag-required":          decl.Breaking,
	"flag-group-added":       decl.Breaking,
	"command-usage-changed":  decl.Dangerous,
	"flag-group-removed":     decl.Safe,
	"flag-default-changed":   decl.Dangerous,
	"command-hidden":         decl.Dangerous,
	"command-deprecated":     decl.Dangerous,
	"flag-hidden":            decl.Dangerous,
	"flag-deprecated":        decl.Dangerous,
	"command-added":          decl.Safe,
	"alias-added":            decl.Safe,
	"flag-added":             decl.Safe,
	"flag-shorthand-added":   decl.Safe,
}

// Compare classifies every difference between the baseline tree and the head tree.
func Compare(base, head Tree) []decl.Change {
	var out []decl.Change
	add := func(c, kind, detail string) {
		if _, ok := Kinds[kind]; !ok {
			// An unlisted kind would take the zero severity, Safe: a break that passes.
			panic("clitree: change kind " + kind + " is not in Kinds")
		}
		out = append(out, decl.Change{Surface: Surface, Coord: c, Kind: kind, Severity: Kinds[kind], Detail: detail})
	}
	bm, hm := index(base), index(head)

	for _, p := range sortedKeys(bm) {
		b := bm[p]
		h, ok := hm[p]
		if !ok {
			add(coord(p), "command-removed", fmt.Sprintf("`%s` is no longer a command", display(p)))
			continue
		}
		for _, a := range b.Aliases {
			if !contains(h.Aliases, a) {
				add(coord(p)+"@"+a, "alias-removed", fmt.Sprintf("`%s` no longer answers to %q", display(p), a))
			}
		}
		for _, a := range h.Aliases {
			if !contains(b.Aliases, a) {
				add(coord(p)+"@"+a, "alias-added", "")
			}
		}
		if h.Hidden && !b.Hidden {
			add(coord(p), "command-hidden", "no longer listed in help")
		}
		if h.Deprecated != "" && b.Deprecated == "" {
			add(coord(p), "command-deprecated", h.Deprecated)
		}
		if h.Use != b.Use {
			add(coord(p), "command-usage-changed", fmt.Sprintf("%q -> %q; check the positional arguments it accepts", b.Use, h.Use))
		}
		bf, hf := flagIndex(b), flagIndex(h)
		for _, n := range sortedKeys(bf) {
			f := bf[n]
			g, ok := hf[n]
			c := flagCoord(p, n)
			if !ok {
				add(c, "flag-removed", fmt.Sprintf("`%s` no longer accepts --%s", display(p), n))
				continue
			}
			if f.Type != g.Type {
				add(c, "flag-type-changed", fmt.Sprintf("%s -> %s", f.Type, g.Type))
			}
			if f.Shorthand != g.Shorthand {
				switch {
				case f.Shorthand == "":
					add(c, "flag-shorthand-added", "-"+g.Shorthand)
				default:
					add(c, "flag-shorthand-changed", fmt.Sprintf("%s -> %s", short(f.Shorthand), short(g.Shorthand)))
				}
			}
			if f.Default != g.Default {
				add(c, "flag-default-changed", fmt.Sprintf("%q -> %q", f.Default, g.Default))
			}
			if g.Hidden && !f.Hidden {
				add(c, "flag-hidden", "no longer listed in help")
			}
			if g.Deprecated != "" && f.Deprecated == "" {
				add(c, "flag-deprecated", g.Deprecated)
			}
			if g.Required && !f.Required {
				add(c, "flag-required", fmt.Sprintf("`%s` now refuses to run without --%s", display(p), n))
			}
			for _, grp := range g.Groups {
				if !contains(f.Groups, grp) {
					add(c+"#"+strings.ReplaceAll(grp, " ", ","), "flag-group-added", "now constrained: "+grp)
				}
			}
			for _, grp := range f.Groups {
				if !contains(g.Groups, grp) {
					add(c+"#"+strings.ReplaceAll(grp, " ", ","), "flag-group-removed", "")
				}
			}
		}
		for _, n := range sortedKeys(hf) {
			if _, ok := bf[n]; !ok {
				add(flagCoord(p, n), "flag-added", "")
			}
		}
	}
	for _, p := range sortedKeys(hm) {
		if _, ok := bm[p]; !ok {
			add(coord(p), "command-added", "")
		}
	}
	decl.Sort(out)
	return out
}

func short(s string) string {
	if s == "" {
		return "(none)"
	}
	return "-" + s
}

func display(p string) string {
	if p == "" {
		return Surface
	}
	return Surface + " " + p
}

func index(t Tree) map[string]Command {
	m := make(map[string]Command, len(t.Commands))
	for _, c := range t.Commands {
		m[c.Path] = c
	}
	return m
}

func flagIndex(c Command) map[string]Flag {
	m := make(map[string]Flag, len(c.Flags))
	for _, f := range c.Flags {
		m[f.Name] = f
	}
	return m
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
