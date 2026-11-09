// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"k8s.io/client-go/kubernetes"
)

// Two orderings `dcctl upgrade` used to take on trust: the cluster-level `dcctl install` runs
// first, and a newer dcctl is not pointed at an older release.
//
// 🔴 THE OPERATOR IDENTITY CHECK (RequireOperator) CANNOT SEE EITHER. It digests the CRDs, and a
// release that changes the controller but not the definitions leaves the identity where it was,
// so a cluster still on the previous release's controller passes it. The cluster's own witness
// of which dcctl last completed an install is the install record, so that is what is asked.

// releaseVersion is a release tag taken apart: the major.minor.patch core and the pre-release
// identifiers after the first hyphen (empty for a final release).
type releaseVersion struct {
	core [3]int
	pre  []string
}

var releaseSemver = regexp.MustCompile(`^v?([0-9]+)\.([0-9]+)\.([0-9]+)(?:-([0-9A-Za-z.-]+))?(?:\+.*)?$`)

// parseRelease reports false for anything that is not a release tag ("dev", a local build's
// free-form tag, the empty string).
func parseRelease(v string) (releaseVersion, bool) {
	m := releaseSemver.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return releaseVersion{}, false
	}
	var r releaseVersion
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return releaseVersion{}, false
		}
		r.core[i] = n
	}
	if m[4] != "" {
		r.pre = strings.Split(m[4], ".")
	}
	return r, true
}

// compare orders two releases by semantic versioning: the core, then a final release above any
// pre-release of the same core, then the pre-release identifiers (numeric ones numerically).
func (a releaseVersion) compare(b releaseVersion) int {
	for i := 0; i < 3; i++ {
		if a.core[i] != b.core[i] {
			if a.core[i] < b.core[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a.pre) == 0 && len(b.pre) == 0:
		return 0
	case len(a.pre) == 0:
		return 1
	case len(b.pre) == 0:
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		x, y := a.pre[i], b.pre[i]
		xn, xerr := strconv.Atoi(x)
		yn, yerr := strconv.Atoi(y)
		switch {
		case xerr == nil && yerr == nil:
			if xn != yn {
				if xn < yn {
					return -1
				}
				return 1
			}
		case xerr == nil:
			return -1 // numeric identifiers sort below alphanumeric ones
		case yerr == nil:
			return 1
		case x != y:
			if x < y {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a.pre) < len(b.pre):
		return -1
	case len(a.pre) > len(b.pre):
		return 1
	}
	return 0
}

// installWitness is what the cluster's install record says about the last install.
type installWitness struct {
	// Found is false when the cluster has no install record.
	Found bool
	// Applying is true when an install started and did not finish.
	Applying bool
	// DcctlVersion is the dcctl of the last COMPLETED install (empty when unknown).
	DcctlVersion string
}

// readInstallWitness reads only what the order check needs, and deliberately not through
// readInstallRecord: that one validates the whole record against the live cluster (schema,
// outputs, cluster UID), and a record from an older dcctl is exactly the one that must still
// answer "which version installed this".
func readInstallWitness(ctx context.Context, typed kubernetes.Interface) (installWitness, error) {
	cm, err := getInstallRecordMap(ctx, typed)
	if err != nil {
		return installWitness{}, err
	}
	if cm == nil {
		return installWitness{}, nil
	}
	w := installWitness{Found: true}
	var rec InstallRecord
	if err := json.Unmarshal([]byte(cm.Data[installRecordKey]), &rec); err != nil {
		return w, nil // found but unreadable: DcctlVersion stays unknown
	}
	if rec.Phase == installPhaseApplying {
		w.Applying = true
	}
	if last := rec.lastCompleted(); last != nil {
		w.DcctlVersion = last.DcctlVersion
	}
	return w, nil
}

// judgeInstallOrder refuses an upgrade by a dcctl that is newer than the one that last completed
// the cluster-level install, or when that cannot be told. Equal is fine (a re-run at the same
// version), and so is an install by a NEWER dcctl (an older one going back is the documented
// downgrade path). A dcctl built from source carries no release to compare and is let through.
func judgeInstallOrder(w installWitness, dcctlVersion, installCommand string) error {
	if dcctlVersion == "dev" || IsUnpublishedImageVersion(dcctlVersion) {
		return nil
	}
	ours, ok := parseRelease(dcctlVersion)
	if !ok {
		return fmt.Errorf("this dcctl carries no release version (%q), so whether the cluster-level install "+
			"is current for it cannot be judged; use a released dcctl or one built from source", dcctlVersion)
	}
	first := fmt.Sprintf("Run the cluster-level install first, then this again:\n\n    %s\n\n"+
		"  The install moves the operator and its definitions, which every instance on the cluster shares; "+
		"`dcctl upgrade` moves one instance and never moves them. Give the install the settings the cluster "+
		"was installed with (such as --ha or --compact), which it refuses to change under a running instance. "+
		"Nothing has been changed.", installCommand)
	switch {
	case !w.Found:
		return fmt.Errorf("this cluster has no install record, so there is no telling whether `dcctl install` "+
			"has been run with this dcctl (%s). %s", dcctlVersion, first)
	case w.Applying && w.DcctlVersion == "":
		return fmt.Errorf("an install of this cluster started and did not finish. %s", first)
	}
	theirs, ok := parseRelease(w.DcctlVersion)
	if !ok {
		return fmt.Errorf("this cluster's install record does not say which dcctl installed it (%q), so it "+
			"cannot be shown to be current for this one (%s). %s", w.DcctlVersion, dcctlVersion, first)
	}
	if theirs.compare(ours) < 0 {
		return fmt.Errorf("this cluster was installed by dcctl %s and this dcctl is %s, so the operator it "+
			"carries may be older than the release this upgrade moves the instance to. %s",
			w.DcctlVersion, dcctlVersion, first)
	}
	if w.Applying {
		return fmt.Errorf("an install of this cluster by a newer dcctl started and did not finish. %s", first)
	}
	return nil
}

// requireInstallOrderFor is the seam hydrateUpgradeState reads through, for the reason
// requireOperatorFor is one.
var requireInstallOrderFor = func(ctx context.Context, typed kubernetes.Interface, dcctlVersion, installCommand string) error {
	if dcctlVersion == "dev" || IsUnpublishedImageVersion(dcctlVersion) {
		return nil // nothing to compare; spare the read
	}
	w, err := readInstallWitness(ctx, typed)
	if err != nil {
		return err
	}
	return judgeInstallOrder(w, dcctlVersion, installCommand)
}

// refuseForeignRelease refuses an explicit `--version` that is not this dcctl's release or
// something this dcctl can be told apart from, in either direction.
//
// 🔴 THE CHART AND THE INFRASTRUCTURE CONFIGURATION ARE THIS DCCTL'S, so asking it for older
// images plans its newer chart around services that have never seen its configuration; to go
// back, where that release's notes allow it, use the older release's own dcctl, which carries
// the matching chart.
//
// 🔴 A NEWER ONE IS REFUSED FOR A DIFFERENT REASON, AND ITS REMEDY IS NOT `dcctl install`. The
// install record names the dcctl that ran the install (this binary's own version), never the
// --version it was given, so an install by this dcctl at a newer --version records THIS dcctl
// and the upgrade would ask for it again, for ever. The newer release's own dcctl is the only
// thing that records it.
//
// Only an EXPLICIT version is judged: the version a plain upgrade stays on is the instance's.
// Equal to this dcctl's release is the ordinary case; a tag that is not a release (a local
// build, any tag on a registry of the operator's own) and a dcctl built from source are not
// compared.
func refuseForeignRelease(requested, ownRelease, registry, retry string) error {
	return judgeForeignRelease("--version "+requested, requested, ownRelease, registry, retry, false)
}

// normalizeRegistry reduces the spellings of one registry to a comparable form: surrounding
// space, an https:// scheme, trailing slashes, the host's case and the default :443 are not
// differences. Anything else is left alone, so a genuinely different registry stays different.
func normalizeRegistry(r string) string {
	r = strings.ToLower(strings.TrimSpace(r))
	r = strings.TrimPrefix(r, "https://")
	r = strings.TrimRight(r, "/")
	host, rest, hasPath := strings.Cut(r, "/")
	host = strings.TrimSuffix(host, ":443")
	if hasPath {
		return host + "/" + rest
	}
	return host
}

// judgeForeignRelease is refuseForeignRelease for a version that may not have been asked for with
// --version: subject names where it came from, and olderOK lets a version older than this dcctl
// through (an instance's declared version, which a flagless upgrade keeps).
func judgeForeignRelease(subject, requested, ownRelease, registry, retry string, olderOK bool) error {
	// 🔴 A TAG ON A REGISTRY OF THE OPERATOR'S OWN IS WHATEVER THEY PUSHED, and the upgrade drill
	// pushes semver-shaped ones (0.24.0-drill.1). Only the published registry's tags are releases,
	// and it is recognised by every spelling of it (ghcr.io:443/..., a trailing slash), not only
	// the literal constant.
	if requested == "" || (registry != "" && normalizeRegistry(registry) != normalizeRegistry(DefaultImageRegistry)) {
		return nil
	}
	want, ok := parseRelease(requested)
	if !ok {
		return nil
	}
	if ownRelease == "" {
		return fmt.Errorf("this dcctl carries no image version at all, so whether %s is a move it "+
			"can make cannot be judged; use a released dcctl, or one built from source", subject)
	}
	if IsUnpublishedImageVersion(ownRelease) {
		return nil
	}
	own, ok := parseRelease(ownRelease)
	if !ok {
		return nil
	}
	switch c := want.compare(own); {
	case c > 0:
		return fmt.Errorf("%s is newer than this dcctl (%s). Get the dcctl of release %s, run "+
			"`dcctl install` with it, then upgrade. A dcctl installs and upgrades only to its own release: "+
			"the install is recorded against the dcctl that ran it, so running it here at %s would not "+
			"satisfy the upgrade. Nothing has been changed", subject, ownRelease, requested, requested)
	case c < 0 && !olderOK:
		return fmt.Errorf("%s is older than this dcctl (release %s) and a dcctl only moves an instance "+
			"to its own release: it would plan its own newer chart and infrastructure configuration "+
			"around the older images. To go back, where that release's notes allow it, use the dcctl "+
			"of release %s (`%s` with that binary). Nothing has been changed",
			subject, ownRelease, requested, retry)
	}
	return nil
}

// RefuseForeignRelease is refuseForeignRelease for this build's own release, for the command
// layer (`dcctl install --version`).
func RefuseForeignRelease(requested, registry, retry string) error {
	return refuseForeignRelease(requested, DefaultImageVersion, registry, retry)
}
