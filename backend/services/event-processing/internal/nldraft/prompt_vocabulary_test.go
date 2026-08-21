// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package nldraft

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-event-processing/internal/detect/predicate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The drafting prompt is a SECOND declaration of the CEL vocabulary, and the compiler's
// environment is the first. Nothing made them agree, and they did not: `geo` and its
// containment function shipped in the environment as schema v3 while this prompt kept
// telling the model "No other identifiers exist in either" over a list of five — so the
// one door meant to make geofencing discoverable actively asserted it did not exist.
//
// 🔴 A DRIFT LIKE THAT CANNOT BE CAUGHT BY TESTING THE PROMPT AGAINST ITSELF. What follows
// tests it against the environment, in the only two ways that actually bind:
//
//   - every identifier the environment declares must be INTRODUCED in the prompt, and
//   - predicate.SchemaVersion must be the version this prompt was written against.
//
// The second is what makes the first complete. A test that walks a hand-written list can
// only ever check the identifiers somebody remembered to add to it — the next variable
// added to the environment would be missing from the list and from the prompt together,
// and the test would pass. But the environment's own doc contract is "Bump on any change
// to the declarations below", so pinning the version means the person adding a variable
// is stopped HERE, at the prompt that has to describe it, rather than shipping a sixth
// identifier the AI door denies the existence of.
//
// If you are here because this test failed after bumping SchemaVersion: add the new
// identifier to the vocabulary section of systemPromptBase, add it to declaredIdentifiers
// below, and then update the pin. In that order — the pin is the reminder, not the work.
const promptWrittenForSchemaVersion = 3

// declaredIdentifiers is every variable predicate.Env() binds, by the constant rather than
// by a string literal, so renaming one in the environment breaks this compile instead of
// silently passing against a stale copy of its old spelling.
var declaredIdentifiers = []string{
	predicate.VarDevice,
	predicate.VarAnchors,
	predicate.VarOccurred,
	predicate.VarM,
	predicate.VarAttr,
	predicate.VarGeo,
}

func TestPromptSchemaVersionPin(t *testing.T) {
	require.Equal(t, promptWrittenForSchemaVersion, predicate.SchemaVersion,
		"the DETECT predicate environment declares schema v%d but this prompt was written "+
			"for v%d. A schema bump means the declared vocabulary changed; describe the change "+
			"in systemPromptBase (and in declaredIdentifiers) before moving the pin.",
		predicate.SchemaVersion, promptWrittenForSchemaVersion)
}

func TestPromptIntroducesEveryDeclaredIdentifier(t *testing.T) {
	vocab := vocabularySection(t)
	for _, id := range declaredIdentifiers {
		// Introduced, not merely mentioned. The prompt's own format glosses each identifier
		// in parentheses ("attr (map of device-attribute key to number)"), and requiring the
		// gloss is what keeps this from passing on an accident: a bare substring test for
		// "m" or "attr" matches most English sentences in the section, so it would report
		// green for a vocabulary line that never named them.
		assert.Containsf(t, vocab, id+" (",
			"the CEL vocabulary section never introduces %q, which predicate.Env() declares. "+
				"A model told an identifier does not exist will not use it.", id)
	}
}

// TestPromptDocumentsTheContainmentFunction covers what the identifier list structurally
// cannot: `geo` is an opaque value whose entire usefulness is one function on it, so naming
// the variable without naming the call teaches the model nothing it can write.
func TestPromptDocumentsTheContainmentFunction(t *testing.T) {
	call := predicate.VarGeo + "." + predicate.FuncInFence
	assert.Contains(t, systemPromptBase, call,
		"the prompt names the geo binding but never shows the %s call, which is the only "+
			"operation it has", call)
}

// TestGuardVocabularyRejectionListCoversEveryEventIdentifier checks the OTHER direction.
// The guard environment is a different, much smaller vocabulary, and the prompt warns which
// identifiers are rejected there. That warning is only useful while it is complete: an
// identifier declared for `when` but missing from the rejection list is exactly the one a
// model will try in a guard.
func TestGuardVocabularyRejectionListCoversEveryEventIdentifier(t *testing.T) {
	const marker = "A guard naming "
	i := strings.Index(systemPromptBase, marker)
	require.NotEqual(t, -1, i, "the guard rejection sentence is gone; this test is measuring nothing")
	line := systemPromptBase[i:]
	if end := strings.Index(line, "\n"); end != -1 {
		line = line[:end]
	}
	for _, id := range declaredIdentifiers {
		assert.Containsf(t, line, id,
			"the guard rejection sentence omits %q, which IS declared for \"when\" and is "+
				"therefore precisely what a model would reach for in a guard: %s", id, line)
	}
}

// vocabularySection returns the prompt text describing the `when.cel` vocabulary — from the
// heading down to the guard paragraph that begins the next section.
func vocabularySection(t *testing.T) string {
	t.Helper()
	const start = `CEL vocabulary inside a "when.cel"`
	const end = `An action "guard" is evaluated LATER`
	i := strings.Index(systemPromptBase, start)
	require.NotEqualf(t, -1, i, "the vocabulary heading %q is gone from the prompt; this test "+
		"would otherwise silently measure an empty string", start)
	rest := systemPromptBase[i:]
	j := strings.Index(rest, end)
	require.NotEqualf(t, -1, j, "the guard paragraph %q is gone from the prompt", end)
	return rest[:j]
}
