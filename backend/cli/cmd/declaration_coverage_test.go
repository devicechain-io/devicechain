// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"sort"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

// disposition says where a bootstrap flag's value ends up, and every flag must have
// one.
type disposition string

const (
	// declared: the value, or the thing it decides, is a field of InstanceSpec. A
	// second operator on another machine can converge to the same instance from the
	// declaration alone.
	declared disposition = "recorded in the instance declaration"

	// runScoped: a property of the RUN, not of the instance. Recording it would be
	// wrong, not merely unnecessary — a rehearsal flag or a filesystem path is not a
	// fact about the deployed system, and the declaration explicitly bans paths.
	runScoped disposition = "a property of this run, not of the instance"

	// secretHalf: the value is a credential. The declaration is cluster-scoped and
	// its own rule is "credentials live in Secrets, and what a consumer gets is a
	// name", so this may never be a field.
	secretHalf disposition = "a credential; lives in a Secret, never in the declaration"

	// excluded: deliberately NOT recorded, with the reason written down in the CR's
	// own doc comment. Different from a gap: somebody decided.
	excluded disposition = "deliberately excluded, with a stated reason"

	// 🔴 gap: this flag CHANGES WHAT GETS DEPLOYED and is recorded nowhere dcctl
	// reads back. The declaration is therefore silently wrong about the instance —
	// the exact failure its doc comment names. Each one below is a known, filed
	// defect, listed so that closing one is a deliberate edit and opening a NEW one
	// is impossible without writing it down.
	gap disposition = "🔴 KNOWN GAP — changes what is deployed, recorded nowhere"
)

// 🔴 THE POINT OF THIS TEST IS THAT THE LIST IS NOT THE SOURCE. It is checked
// AGAINST cobra's own flag registry, which is where flags actually come from, so a
// flag nobody added here fails the build instead of being invisible.
//
// That distinction is the whole reason this file exists. The test it replaces,
// TestEveryDeploymentAlteringFlagIsRecorded, asserted the same property from a
// hand-written list of eight flags against a command that registers far more — so
// it could detect a BROKEN field and never a MISSING one, which is precisely the
// failure it was written to prevent. The declaration's own doc comment says a field
// that changes what gets deployed and is not recorded "does not make the object
// safer — it makes it wrong, silently", and three such fields had accumulated
// underneath a green test named for exactly that rule.
//
// 🔑 A list cannot see the entry nobody wrote. Ask the registry instead.
var dispositions = map[string]disposition{
	// --- recorded in the declaration ---
	"kube-context":  declared, // resolves the cluster binding, which is recorded
	"profile":       declared,
	"registry":      declared,
	"version":       declared,
	"build":         declared, // decides the image source, and that is recorded
	"host":          declared,
	"no-tls":        declared,
	"no-monitoring": declared,
	"no-cnpg":       declared,
	"grafana-sso":   declared,
	"compact":       declared,
	"ha":            declared,
	"enable-area":   declared,
	"dev":           declared, // a preset; every flag it expands to is itself declared

	// --- properties of the run ---
	"dry-run":                 runScoped,
	"yes":                     runScoped,
	"skip-preflight":          runScoped,
	"allow-legacy-db-removal": runScoped, // an assertion about THIS run's data handling
	"escrow-file":             runScoped, // a filesystem path; the declaration bans paths
	"escrow-passphrase-file":  runScoped,
	"no-escrow":               runScoped, // whether THIS run wrote a second copy of the key
	"restore-root-key":        runScoped, // a filesystem path

	// --- deliberately excluded, reason written in instance_types.go ---
	//
	// "that a restore happened belongs to anyone reading this, while WHERE the
	// archive was is one operator's command line." Restored + RestoredAt record the
	// fact; the coordinates are not recorded on purpose.
	"restore-rdb-from":  excluded,
	"restore-rdb-at":    excluded,
	"restore-tsdb-from": excluded,
	"restore-tsdb-at":   excluded,

	// --- known gaps ---
	//
	// backup-credentials-file carries BOTH halves in one file. The access key and
	// secret key are secretHalf and already have a home (dc-backup-credentials, an
	// owned Secret). The endpoint URL and the two bucket names are not secrets, they
	// decide where every WAL segment is shipped, and nothing dcctl reads records
	// them — so a run that omits the flag reverts the destination to in-cluster and
	// retargets both live archivers at an empty bucket.
	"backup-credentials-file": gap,

	// lwm2m-identities is two halves too. The pre-shared keys are secretHalf. The
	// tenancy binding beside them — tenant, externalId, deviceTypeToken,
	// autoRegister — is provisioning state, not secret, and equally unrecorded. Both
	// survive today only by being copied out of the previous Helm release.
	"lwm2m-identities": gap,
}

// The gaps, stated separately so the test can assert the set EXACTLY. Closing one
// means deleting it from here, which is a deliberate edit; opening a new one means
// adding it, which is a sentence somebody has to write.
var knownGaps = []string{
	"backup-credentials-file",
	"lwm2m-identities",
}

func TestEveryBootstrapFlagHasADeclaredDisposition(t *testing.T) {
	var unclassified []string
	bootstrapCmd.Flags().VisitAll(func(f *pflag.Flag) {
		if _, ok := dispositions[f.Name]; !ok {
			unclassified = append(unclassified, f.Name)
		}
	})
	sort.Strings(unclassified)

	if len(unclassified) > 0 {
		t.Errorf(`these bootstrap flags have no disposition: %s

Every flag has to be classified, because the instance declaration's rule is that a
field which changes what gets deployed and is NOT recorded there makes the
declaration silently wrong. Add each one to the dispositions map above:

  declared   - the value, or what it decides, is a field of InstanceSpec
  runScoped  - a property of this run: a rehearsal flag, a filesystem path, an assertion
  secretHalf - a credential, which belongs in a Secret and never in a cluster-scoped CR
  excluded   - deliberately not recorded, and say WHY
  gap        - it changes what is deployed and nothing records it; also add it to knownGaps

Choosing 'gap' is allowed. Choosing nothing is not.`, strings.Join(unclassified, ", "))
	}
}

// The counterweight, and it is the half that keeps the map honest. Without it the
// map could drift into a list of flags that no longer exist, and a stale entry is
// how a classification survives the removal of the thing it classified.
func TestNoDispositionNamesAFlagThatIsGone(t *testing.T) {
	live := map[string]bool{}
	bootstrapCmd.Flags().VisitAll(func(f *pflag.Flag) { live[f.Name] = true })

	var stale []string
	for name := range dispositions {
		if !live[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("these dispositions name flags that no longer exist: %s. Remove them — a "+
			"classification for a flag nobody can pass is a sentence that reads as coverage",
			strings.Join(stale, ", "))
	}
}

// 🔴 THE GAPS ARE ASSERTED EXACTLY, NOT AS A FLOOR. A test that only checked the
// listed gaps are still gaps would pass while a fourth one appeared beside them.
func TestTheKnownGapsAreExactlyTheseAndNoMore(t *testing.T) {
	var found []string
	for name, d := range dispositions {
		if d == gap {
			found = append(found, name)
		}
	}
	sort.Strings(found)
	want := append([]string(nil), knownGaps...)
	sort.Strings(want)

	if strings.Join(found, ",") != strings.Join(want, ",") {
		t.Errorf(`the set of known declaration gaps moved.
  knownGaps says: %s
  dispositions:   %s

If you CLOSED one, delete it from both. If you OPENED one, that is a defect being
filed rather than fixed — say so in the commit, because a flag whose value the
declaration does not record makes the declaration wrong for everyone who reads it.`,
			strings.Join(want, ", "), strings.Join(found, ", "))
	}
}
