// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Pure helpers for reconciling a device's PUBLISHED command vocabulary against the
// DRAFT definitions authored on its profile (ADR-043 decision 3 / ADR-045).
//
// Kept out of the components so the reconciliation — which is where the publish
// boundary is either respected or quietly lost — is unit-tested on its own.

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
