// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"

	assets "github.com/devicechain-io/dc-deploy"
)

// varDeclRe matches a top-level `variable "name" {` declaration. Anchored to the
// start of a line because that is where a root declares one; a nested match would
// be inside a string or a heredoc.
var varDeclRe = regexp.MustCompile(`(?m)^variable\s+"([^"]+)"\s*\{`)

// rootDeclaredVars reads the variable names a root declares, from the EMBEDDED
// tree — the same bytes dcctl extracts and applies, rather than a copy on disk that
// a maintainer checkout may have edited.
func rootDeclaredVars(root fs.FS) (map[string]bool, error) {
	src, err := fs.ReadFile(root, "variables.tf")
	if err != nil {
		return nil, fmt.Errorf("reading a root's variables.tf: %w", err)
	}
	names := map[string]bool{}
	for _, m := range varDeclRe.FindAllStringSubmatch(string(src), -1) {
		names[m[1]] = true
	}
	if len(names) == 0 {
		// The parser's own control. A regex that matched nothing would report that
		// the root declares NOTHING, and splitVars would then refuse every variable
		// — loudly, which is the right direction, but the message would blame the
		// caller for a fault in this function.
		return nil, fmt.Errorf("no variable declarations parsed out of a root's variables.tf")
	}
	return names, nil
}

// splitVars partitions `name=value` assignments between the two roots by which one
// DECLARES each name.
//
// 🔴 WHY THIS IS COMPUTED AND NOT TWO HAND-WRITTEN LISTS. Every value dcctl passes
// to OpenTofu is decided in one place (infraVars), and the split does not change
// which value is right — only which root is asked to receive it. A second, manual
// list of "these go to the cluster" would be a copy of the roots' own variables.tf
// that nothing keeps in step: move a variable between roots and the apply starts
// failing with "Value for undeclared variable", which is at least loud, or — far
// worse — the variable is dropped from the root that now needs it and the apply
// silently uses that root's DEFAULT.
//
// 🔴 A VARIABLE DECLARED BY NEITHER ROOT IS AN ERROR, NOT A DROP. That is the whole
// reason this returns an error at all. Filtering by "is it declared here?" makes an
// unrecognised name vanish quietly from BOTH applies, and the symptom is an instance
// built at a default nobody chose — a --compact install taking full-size volumes, a
// restore flag that restores nothing. The name a caller got wrong, or a variable
// deleted from a root while dcctl still passes it, has to stop the run.
//
// A variable both roots declare is passed to both, deliberately: `namespace` and
// `ha` are genuinely the same question asked of each half, and a value that reached
// only one of them would mean the two halves of one instance disagreed about it.
func splitVars(all []string) (cluster, instance []string, err error) {
	clusterVars, err := rootDeclaredVars(assets.OpenTofuCluster())
	if err != nil {
		return nil, nil, err
	}
	instanceVars, err := rootDeclaredVars(assets.OpenTofuInstance())
	if err != nil {
		return nil, nil, err
	}

	var orphans []string
	for _, assignment := range all {
		name, _, found := strings.Cut(assignment, "=")
		if !found {
			return nil, nil, fmt.Errorf("infrastructure variable %q is not name=value", assignment)
		}
		known := false
		if clusterVars[name] {
			cluster = append(cluster, assignment)
			known = true
		}
		if instanceVars[name] {
			instance = append(instance, assignment)
			known = true
		}
		if !known {
			orphans = append(orphans, name)
		}
	}
	if len(orphans) > 0 {
		sort.Strings(orphans)
		return nil, nil, fmt.Errorf(
			"dcctl passes %d infrastructure variable(s) that NEITHER OpenTofu root declares: %s.\n"+
				"This is refused rather than ignored because ignoring it is silent: the apply would "+
				"run with that root's DEFAULT instead of the value dcctl computed, and the instance "+
				"would come up built to a setting nobody chose. Either the name is wrong here, or the "+
				"variable was removed from a root that still needs it",
			len(orphans), strings.Join(orphans, ", "))
	}
	return cluster, instance, nil
}
