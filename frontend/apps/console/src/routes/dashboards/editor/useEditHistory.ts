// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// useEditHistory holds the editor's working copy as an undo/redo history (the pure history in
// @devicechain/dashboards). Every edit is committed with a label naming what it did, so an undo
// can be announced ("Undid move of Tank level").
//
// The history starts at the loaded definition and is reset only when the working copy is replaced
// from outside the editor: a load (the workspace is mounted per dashboard) and a rollback. It is
// kept across a save and a publish: neither changes the working copy, so nothing in the history
// stops being true, and keeping it lets an author back out of an edit they have just saved. An
// undo after a save simply leaves the board with unsaved changes again.

import { useCallback, useRef, useState } from 'react';
import {
  canRedo,
  canUndo,
  commitEdit,
  createEditHistory,
  isDirty,
  rebaseHistory,
  redoEdit,
  sealHistory,
  undoEdit,
  type DashboardDefinition,
  type EditHistory,
} from '@devicechain/dashboards';

// What an edit did, for the announcement. `name` is the widget's, where the edit is to one.
export type EditAction =
  | 'move'
  | 'resize'
  | 'offset'
  | 'delete'
  | 'placement'
  | 'anchor'
  | 'add'
  | 'duplicate'
  | 'paste'
  | 'options'
  | 'datasource'
  | 'scope'
  | 'candidates'
  | 'title'
  | 'canvas'
  | 'fit'
  | 'strip';

export interface EditLabel {
  action: EditAction;
  name?: string;
}

type History = EditHistory<DashboardDefinition, EditLabel>;

// Two definitions are the same edit-wise when they serialize the same: what a save would write.
const sameDefinition = (a: DashboardDefinition, b: DashboardDefinition) => !isDirty(a, b);

export interface EditHistoryControls {
  // Records an edit. Edits with the same `key`, one after another, are one undo step.
  commit: (
    next: DashboardDefinition | ((current: DashboardDefinition) => DashboardDefinition),
    label: EditLabel,
    key?: string,
  ) => void;
  undo: () => EditLabel | undefined;
  redo: () => EditLabel | undefined;
  // Ends the open typing step, so the next edit is a step of its own.
  seal: () => void;
  // Replaces the working copy and forgets the history (a rollback).
  reset: (definition: DashboardDefinition) => void;
  // Applies a change that is not an edit to every state held.
  rebase: (fn: (d: DashboardDefinition) => DashboardDefinition) => void;
  canUndo: boolean;
  canRedo: boolean;
}

export function useEditHistory(loaded: DashboardDefinition): [DashboardDefinition, EditHistoryControls] {
  const [history, setHistory] = useState<History>(() => createEditHistory<DashboardDefinition, EditLabel>(loaded));
  // The latest history, read by undo/redo so they can return the label of the step they took.
  const ref = useRef(history);
  const set = useCallback((next: History) => {
    ref.current = next;
    setHistory(next);
  }, []);

  const commit = useCallback<EditHistoryControls['commit']>(
    (next, label, key) => {
      const h = ref.current;
      const value = typeof next === 'function' ? next(h.present) : next;
      set(commitEdit(h, value, label, { key, equals: sameDefinition }));
    },
    [set],
  );
  const undo = useCallback(() => {
    const step = undoEdit(ref.current);
    set(step.history);
    return step.label;
  }, [set]);
  const redo = useCallback(() => {
    const step = redoEdit(ref.current);
    set(step.history);
    return step.label;
  }, [set]);
  const seal = useCallback(() => set(sealHistory(ref.current)), [set]);
  const reset = useCallback(
    (definition: DashboardDefinition) => set(createEditHistory<DashboardDefinition, EditLabel>(definition)),
    [set],
  );
  const rebase = useCallback((fn: (d: DashboardDefinition) => DashboardDefinition) => set(rebaseHistory(ref.current, fn)), [set]);

  return [
    history.present,
    { commit, undo, redo, seal, reset, rebase, canUndo: canUndo(history), canRedo: canRedo(history) },
  ];
}
