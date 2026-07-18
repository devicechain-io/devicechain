// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Pure helpers for reconciling a device's PUBLISHED command vocabulary against the
// DRAFT definitions authored on its profile (ADR-043 decision 3 / ADR-045).
//
// Kept out of the components so the reconciliation — which is where the publish
// boundary is either respected or quietly lost — is unit-tested on its own.

// A minimal view of what a command picker needs. Both PublishedCommand and
// CommandDefinition satisfy it, which is the point: which one is offered depends on
// whether the profile constrains the vocabulary.
export interface PickableCommand {
  commandKey: string;
  name?: string | null;
  parameterSchema?: string | null;
}

export interface CommandChoices {
  // The commands the picker may offer — ones the enqueue gate will accept.
  selectable: PickableCommand[];
  // Keys withheld because they are authored but unpublished, for the picker to name.
  draftOnly: string[];
  // Whether the profile restricts the vocabulary at all.
  constrained: boolean;
}

// commandChoices decides what a command picker offers for a device, from its published
// vocabulary and the drafts authored on its profile.
//
// The rule is the enqueue gate's, not the picker's: offer what the gate will accept.
//
//   - CONSTRAINED — the gate rejects anything outside the published vocabulary, so only
//     published commands are selectable. Drafts are named separately so an author who
//     just wrote one learns it is unpublished rather than concluding it vanished.
//   - UNCONSTRAINED — the gate accepts ANY key (ADR-043 decision 4), so the drafts are
//     perfectly sendable and are offered. Withholding them here would leave every
//     unconstrained device — no profile, never published, or no definitions, which pre-GA
//     is most of them — with an empty picker and nothing to author, for commands that
//     would have worked.
//
// A null vocabulary means the token resolved to no device; a saved dashboard can outlive
// the device it points at. Treated as unconstrained so the panel stays usable enough to
// re-point.
export function commandChoices(
  vocabulary: { constrained: boolean; commands: PickableCommand[] } | null | undefined,
  drafts: PickableCommand[],
): CommandChoices {
  if (vocabulary?.constrained !== true) {
    return { selectable: drafts, draftOnly: [], constrained: false };
  }
  return {
    selectable: vocabulary.commands,
    draftOnly: draftOnlyCommandKeys(vocabulary.commands, drafts),
    constrained: true,
  };
}

// draftOnlyCommandKeys returns the keys an author has written down but not yet
// published, so a picker offering published commands can NAME what it is withholding.
//
// The comparison is by COMMAND KEY, not by token or id: the key is what the enqueue gate
// matches on, and it is what makes a draft "the same command" as its published copy. A
// draft that edits an already-published command is therefore not draft-only — its
// published version is selectable, just at the older definition.
//
// Matching is exact, including case, mirroring the gate (a mis-cased key is a different
// command). Duplicate keys collapse, and order follows the draft list so the hint reads
// in the order the author sees on the profile.
export function draftOnlyCommandKeys(
  published: readonly { commandKey: string }[],
  drafts: readonly { commandKey: string }[],
): string[] {
  const publishedKeys = new Set(published.map((c) => c.commandKey));
  const seen = new Set<string>();
  const out: string[] = [];
  for (const draft of drafts) {
    if (publishedKeys.has(draft.commandKey)) continue;
    if (seen.has(draft.commandKey)) continue;
    seen.add(draft.commandKey);
    out.push(draft.commandKey);
  }
  return out;
}
