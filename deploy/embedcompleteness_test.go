// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package assets

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// 🔴 THIS IS THE GATE THE OTHER EMBED TESTS CANNOT BE. Every one of them —
// TestOpenTofuComplete, TestOpenTofuEmbedsNonTerraformModuleFiles,
// TestHelmChartComplete — asks "does the embedded set CONTAIN these named files?"
// A positive allowlist answers a question nobody is in danger of getting wrong.
// The dangerous question is the other one: "is there anything ON DISK that did
// NOT make it into the binary?"
//
// Nothing answers that today, and the cost is asymmetric. go:embed fails the
// build only when a pattern matches NOTHING; a pattern that still matches
// something while silently missing a new file is not an error, not a warning,
// and not visible in a source checkout — where the file is right there on disk
// and every command that reads it works. It fails on a USER's machine, at
// `dcctl bootstrap`, against a repo whose tests are green.
//
// The cnpg-cluster chart already walked into this once. Its fix
// (TestOpenTofuEmbedsNonTerraformModuleFiles) names three specific files, which
// closed the INSTANCE and left the CLASS open. The next one through is a second
// OpenTofu root: `opentofu/*.tf` does not cross a `/`, so a new
// `opentofu/<root>/main.tf` matches no pattern, both existing patterns still
// match, the build succeeds, all three allowlist tests still pass, and dcctl
// ships without a root it needs.
//
// 🔑 THE INVARIANT IS A DENY-BY-DEFAULT, AND THAT IS THE WHOLE POINT. Every file
// under deploy/opentofu either ships or is NAMED here as deliberately withheld.
// There is no third category, so a new file cannot arrive by accident: it either
// travels in the binary or it fails this test until someone decides which it is.

// deliberatelyNotShipped are the files under deploy/opentofu that are NOT meant
// to travel in the binary and are not secrets either — documentation for someone
// reading the repo, which dcctl has no use for at runtime.
//
// Keep this list SHORT and keep it honest. An entry here is an exemption from the
// only check that can see a missing asset, so adding one should feel like a
// decision. Each is verified to exist below: a stale entry would silently widen
// the exemption to cover a file that arrives with that name later.
var deliberatelyNotShipped = map[string]string{
	"README.md":                "operator-facing documentation for the tree; dcctl never reads it",
	"terraform.tfvars.example": "a commented template for hand-runs; dcctl passes every var with -var",
}

// 🔑 BOTH ENTRIES SIT AT THE TREE TOP, AND THAT IS THE RULE RATHER THAN A
// COINCIDENCE: a ROOT directory holds only what tofu needs, and documentation
// lives beside the roots instead of inside one.
//
// The roots are embedded with `all:`, which takes everything in the directory.
// That bluntness is deliberate — it is what removes the per-extension glob whose
// silent miss this whole test exists for — but it means a file placed inside a
// root SHIPS, with no say in the matter. When terraform.tfvars.example was moved
// into instance/ during the layout change, it began shipping for the first time,
// and TestNoSecretsEmbedded caught it by matching "tfvars" on a substring.
//
// That check is blunt on purpose and must stay blunt: it cannot tell
// `.tfvars.example` from `.tfvars`, and the right response is to keep the
// convenience file out of the root, never to teach a secret-leak test about
// exceptions.

// neverShipped are paths that must be ABSENT from the embedded set. Two kinds,
// and they are excluded for different reasons:
//
//   - terraform.tfstate / terraform.tfvars hold CREDENTIALS in cleartext.
//     TestNoSecretsEmbedded asserts the same thing from the other direction; this
//     predicate exists so the walk below can tell "correctly withheld" from
//     "accidentally missing" without consulting that test.
//   - .terraform/ and .terraform.lock.hcl are machine-local build artifacts. The
//     lock file in particular records provider hashes for the platform that ran
//     `tofu init`, so shipping it would pin a user's install to whichever
//     architecture last built here.
//
// These are all gitignored, so unlike deliberatelyNotShipped they may or may not
// be present in any given checkout — which is why they are matched by predicate
// rather than asserted to exist.
func neverShipped(rel string) bool {
	base := path.Base(rel)
	// 🔑 THE `.terraform` PREFIX COVERS A CLASS, NOT A LIST, and it is a class whose
	// members appear and vanish DURING a run. This was three exact names until
	// `.terraform.tfstate.lock.info` — a lock file that exists only while a tofu
	// command holds the state — turned up mid-test, because a root directory is
	// where tofu runs and something else was running in it. Enumerating tofu's
	// scratch files by name would be a list that is wrong whenever the tool adds
	// one, and wrong in the direction that fails a build for a file nobody ships.
	return strings.HasPrefix(base, "terraform.tfstate") ||
		base == "terraform.tfvars" ||
		strings.HasPrefix(base, ".terraform") ||
		strings.HasPrefix(rel, ".terraform/") ||
		strings.Contains(rel, "/.terraform/")
}

// TestEveryOpenTofuFileOnDiskEitherShipsOrIsNamed walks the real deploy/opentofu
// directory and requires each file to be embedded, deliberately withheld, or a
// secret. It is the only test that can detect an asset the embed globs missed.
func TestEveryOpenTofuFileOnDiskEitherShipsOrIsNamed(t *testing.T) {
	embedded := make(map[string]bool)
	for _, p := range collect(t, OpenTofu()) {
		embedded[p] = true
	}

	// 🔴 A WALK THAT FINDS NOTHING MUST NOT READ AS "EVERYTHING SHIPS". If the
	// working directory ever moves, or the tree is renamed, filepath.WalkDir
	// returns no files and every assertion below is vacuous — the exact
	// gate-that-cannot-fail this test exists to be the opposite of.
	onDisk := 0

	root := "opentofu"
	if err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			// Don't descend into the provider cache; it is large, machine-local
			// and correctly absent from the binary.
			if d.Name() == ".terraform" {
				return filepath.SkipDir
			}
			return nil
		}
		onDisk++

		switch {
		case neverShipped(rel):
			if embedded[rel] {
				t.Errorf("%q IS embedded and must not be — it holds credentials in cleartext or is a local cache", rel)
			}
		case embedded[rel]:
			// Ships. Nothing to say.
		default:
			if _, named := deliberatelyNotShipped[rel]; !named {
				t.Errorf("%q exists under deploy/opentofu but did NOT survive go:embed.\n"+
					"  Nothing else would have told you: `go build` succeeds, every other embed test "+
					"passes, and a source checkout reads the file straight off disk — it fails on a "+
					"user's machine at `dcctl bootstrap`.\n"+
					"  If it belongs in the binary, widen the //go:embed directives in assets.go "+
					"(note `opentofu/*.tf` does NOT cross a `/`, so a new root directory needs its own "+
					"pattern). If it does not, name it in deliberatelyNotShipped with the reason.", rel)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}

	if onDisk == 0 {
		t.Fatalf("walked %q and found no files at all — the test cannot see the tree it is checking, "+
			"so every assertion above was vacuous", root)
	}
}

// TestNothingIsExemptedThatDoesNotExist keeps deliberatelyNotShipped honest.
//
// An exemption for a file that is not there is not harmless: it is a standing
// permission for the NEXT file to arrive under that name and be skipped, and
// nothing would flag it. The list is only as good as its correspondence to the
// tree, and only this test checks that direction.
func TestNothingIsExemptedThatDoesNotExist(t *testing.T) {
	for rel, reason := range deliberatelyNotShipped {
		if reason == "" {
			t.Errorf("deliberatelyNotShipped[%q] has no reason recorded — an exemption without one "+
				"cannot be reviewed", rel)
		}
		if _, err := os.Stat(filepath.Join("opentofu", filepath.FromSlash(rel))); err != nil {
			t.Errorf("deliberatelyNotShipped names %q, which is not in the tree (%v) — remove it, or "+
				"it silently exempts whatever arrives under that name later", rel, err)
		}
	}
}

// TestTheHelmChartShipsWholeToo applies the same deny-by-default walk to the
// chart. Its embed uses `all:helm/devicechain`, which is recursive and has no
// per-extension glob, so there is no known hole here — which is precisely why
// it is worth pinning. The chart is the asset most likely to gain a new file
// type, and "the pattern happens to be broad enough today" is not a property
// anything currently enforces.
func TestTheHelmChartShipsWholeToo(t *testing.T) {
	embedded := make(map[string]bool)
	for _, p := range collect(t, HelmChart()) {
		embedded[p] = true
	}

	root := filepath.Join("helm", "devicechain")
	onDisk := 0
	if err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		onDisk++
		if !embedded[filepath.ToSlash(rel)] {
			t.Errorf("%q exists under %s but did NOT survive go:embed — the chart would be "+
				"incomplete in the binary while whole in the repo", filepath.ToSlash(rel), root)
		}
		return nil
	}); err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	if onDisk == 0 {
		t.Fatalf("walked %q and found no files at all — every assertion above was vacuous", root)
	}
}

// TestTheEmbeddedTreeHasNothingTheDiskDoesNot is the converse walk, and it is
// not symmetry for its own sake: fs.Sub + a stale embed can only ever ADD, but a
// file that is in the binary and not in the tree means the two have come apart,
// and the binary is then shipping something no one can review by reading the
// repo.
func TestTheEmbeddedTreeHasNothingTheDiskDoesNot(t *testing.T) {
	for _, rel := range collect(t, OpenTofu()) {
		if _, err := os.Stat(filepath.Join("opentofu", filepath.FromSlash(rel))); err != nil {
			t.Errorf("%q is embedded in the binary but is not in deploy/opentofu (%v) — the shipped "+
				"tree and the reviewable tree disagree", rel, err)
		}
	}
}

var _ = fs.FS(OpenTofu())
