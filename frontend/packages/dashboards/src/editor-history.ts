// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The editor's undo/redo history: a bounded stack of SNAPSHOTS over the pure editor-model
// transforms. Every state it holds is one the working copy really had, so undo restores a
// definition exactly (never a recomputed approximation) and can only ever bring back a state that
// was on screen before, including one the publish gate blocks only if the author really had it.
//
// Generic over the state and the label, and free of React and the DOM: the console wires it to
// its working copy and keyboard. Kept in its own module, outside anything the viewer imports.
//
//   - `past` holds, newest last, the state BEFORE each edit and the edit's label; `future` holds,
//     newest last, the state an undo left and the label of the edit undone.
//   - An edit committed with a `key` equal to the one still open (the previous commit's) merges
//     into that entry: typing into one field is one step. Undo, redo and `sealHistory` close the
//     key, so the next commit opens a new entry even for the same field.
//   - The depth is bounded; the oldest entries fall off.

export const DEFAULT_HISTORY_LIMIT = 100;

export interface HistoryEntry<T, L> {
  state: T;
  label: L;
}

export interface EditHistory<T, L> {
  readonly past: readonly HistoryEntry<T, L>[];
  readonly present: T;
  readonly future: readonly HistoryEntry<T, L>[];
  // The coalescing key of the last commit, while it may still be extended.
  readonly open?: string;
  readonly limit: number;
}

export function createEditHistory<T, L = string>(present: T, options: { limit?: number } = {}): EditHistory<T, L> {
  return { past: [], present, future: [], limit: Math.max(1, Math.floor(options.limit ?? DEFAULT_HISTORY_LIMIT)) };
}

export interface CommitOptions<T> {
  // Commits with the same key, one after another, are one entry.
  key?: string;
  // When two states are the same edit-wise. Defaults to identity.
  equals?: (a: T, b: T) => boolean;
}

// commitEdit records `next` as the new present. A commit that changes nothing returns the history
// unchanged; a new edit drops whatever could have been redone.
export function commitEdit<T, L>(
  h: EditHistory<T, L>,
  next: T,
  label: L,
  options: CommitOptions<T> = {},
): EditHistory<T, L> {
  const equals = options.equals ?? Object.is;
  if (next === h.present || equals(next, h.present)) return h;
  const last = h.past[h.past.length - 1];
  if (options.key !== undefined && options.key === h.open && last) {
    // Typed back to where the entry started: the entry is no edit at all. The key closes with it,
    // or the next keystroke would merge into the unrelated entry beneath.
    if (equals(next, last.state)) return { past: h.past.slice(0, -1), present: next, future: [], limit: h.limit };
    return { ...h, present: next, future: [] };
  }
  const past = [...h.past, { state: h.present, label }];
  if (past.length > h.limit) past.splice(0, past.length - h.limit);
  return { past, present: next, future: [], open: options.key, limit: h.limit };
}

// sealHistory closes the open key: the next commit is a new entry whatever its key.
export function sealHistory<T, L>(h: EditHistory<T, L>): EditHistory<T, L> {
  if (h.open === undefined) return h;
  const { open: _closed, ...rest } = h;
  return rest;
}

export interface Step<T, L> {
  history: EditHistory<T, L>;
  // The edit that was undone or redone; undefined when there was none.
  label?: L;
}

export function undoEdit<T, L>(h: EditHistory<T, L>): Step<T, L> {
  const last = h.past[h.past.length - 1];
  if (!last) return { history: h };
  return {
    history: {
      past: h.past.slice(0, -1),
      present: last.state,
      future: [...h.future, { state: h.present, label: last.label }],
      limit: h.limit,
    },
    label: last.label,
  };
}

export function redoEdit<T, L>(h: EditHistory<T, L>): Step<T, L> {
  const next = h.future[h.future.length - 1];
  if (!next) return { history: h };
  return {
    history: {
      past: [...h.past, { state: h.present, label: next.label }],
      present: next.state,
      future: h.future.slice(0, -1),
      limit: h.limit,
    },
    label: next.label,
  };
}

export const canUndo = (h: EditHistory<unknown, unknown>): boolean => h.past.length > 0;
export const canRedo = (h: EditHistory<unknown, unknown>): boolean => h.future.length > 0;

// rebaseHistory applies a change that is not an edit (a save retiring the "converted" mark) to
// every state the history holds, so neither undo nor redo can bring the old form back.
export function rebaseHistory<T, L>(h: EditHistory<T, L>, fn: (state: T) => T): EditHistory<T, L> {
  const map = (e: HistoryEntry<T, L>) => ({ ...e, state: fn(e.state) });
  return { ...h, past: h.past.map(map), present: fn(h.present), future: h.future.map(map) };
}
