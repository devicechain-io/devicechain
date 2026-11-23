// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Unicode format characters (category Cf: bidirectional embeddings and overrides U+202A-202E,
// isolates U+2066-2069, zero-width joiners and spaces, the BOM, ...) change how text LOOKS
// without being seen. In a command a person approves they could make the displayed name,
// arguments or rationale read differently from the bytes the device receives, so each one is
// rendered visibly as a backslash-u escape (U+202E shows as the six characters "\u202E") and
// flagged.
const FORMAT_CHARACTER = /\p{Cf}/gu;
const ESCAPE_PREFIX = String.fromCharCode(92) + 'u';

export function revealInvisible(text: string): { text: string; found: boolean } {
  let found = false;
  const out = text.replace(FORMAT_CHARACTER, ch => {
    found = true;
    return ESCAPE_PREFIX + ch.codePointAt(0)!.toString(16).toUpperCase().padStart(4, '0');
  });
  return { text: out, found };
}
