// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// sameLabel -- whether two display strings say the same thing: equal after trimming and
// ignoring case. The ONE predicate behind "this subtitle only repeats the title": the widget
// frame uses it to drop a duplicate subject line, and the layout lint uses it to flag a board
// that would show one, so the two can never disagree about what counts as a repeat. Empty or
// missing strings never match anything.
export function sameLabel(a: string | null | undefined, b: string | null | undefined): boolean {
  if (!a || !b) return false;
  const x = a.trim().toLowerCase();
  return x !== '' && x === b.trim().toLowerCase();
}
