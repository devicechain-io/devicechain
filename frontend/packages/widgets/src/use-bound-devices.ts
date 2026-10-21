// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// useBoundDevices: the host-side hook that keeps a board's device selection and the address in
// step, in both directions, and refuses a deep link it cannot honour.
//
//   address ──► selection   a `bind.<slot>=<device>` link is read, EVERY named device is checked
//                           against the caller's own scope, and only then bound.
//   selection ─► address    a pick (a marker click, a table row, a selector) rewrites the
//                           `bind.*` parameters, so the address in the bar is always a link that
//                           reproduces what is on screen.
//
// 🔴 REPLACE, NOT PUSH, and that is the decision rather than a default. A pick refines the view
// the viewer is already on; it is not a place they went. Pushing an entry per marker click would
// make Back walk through every device they happened to click, on a wall display or a phone,
// instead of leaving the board, and would fill history with entries that all show the same
// board. A copied link still reproduces the state, which is what an address is for. The cost,
// accepted: Back does not undo a pick. What DOES follow history is an address that changes from
// outside (the viewer pastes or follows a different link, or a router entry is restored): that
// is read as a new link and checked like the first one.
//
// 🔴 A REFUSED LINK BLOCKS THE BOARD; it does not fall back to the board's default device. The
// default is a DIFFERENT device from the one the link names, and a viewer who asked for pump 7
// and is shown pump 3 under pump 7's link is being lied to. `phase: 'refused'` carries the
// reasons, the host shows them, and `dismiss` is the viewer's explicit choice to open the board
// without the parts that failed.

import {
  bindParamsOf,
  bindsToSelection,
  parseBindParams,
  selectionToBinds,
  validateBinds,
  withBindParams,
  type BindCheck,
  type BindParamIssue,
  type DashboardDefinition,
  type DeviceResolver,
  type SelectionTarget,
  type SlotBinding,
} from '@devicechain/dashboards';
import { useCallback, useEffect, useRef, useState } from 'react';

// Where the address lives. A host with a router passes its location and a replace-navigate; a
// host without one passes window.location and history.replaceState. `search` has no leading `?`
// requirement: both forms are read.
export interface BoundDevicesAddress {
  search: string;
  replaceSearch: (search: string) => void;
}

export interface LinkProblems {
  issues: BindParamIssue[];
  missing: BindCheck['missing'];
  failed: BindCheck['failed'];
}

export type BoundDevicesPhase = 'ready' | 'checking' | 'refused';

export interface BoundDevices {
  // The selection overlay to hand to useResolvedBindings. Only validated devices are ever in it.
  selection: Record<string, SlotBinding>;
  select: (target: SelectionTarget) => void;
  // Drops the overlay (a mode switch): the board returns to its own defaults.
  reset: () => void;
  phase: BoundDevicesPhase;
  // Present while `phase` is 'refused'.
  problems: LinkProblems | null;
  // Open the board without the parts of the link that failed.
  dismiss: () => void;
  // Ask again (the check failed rather than the device being absent).
  retry: () => void;
}

const NO_PROBLEMS: LinkProblems = { issues: [], missing: [], failed: [] };

function hasBindParams(search: string, definition: DashboardDefinition): boolean {
  const { binds, issues } = parseBindParams(search, definition);
  return Object.keys(binds).length > 0 || issues.length > 0;
}

export function useBoundDevices({
  definition,
  resolver,
  address,
  enabled = true,
}: {
  definition: DashboardDefinition;
  resolver: Pick<DeviceResolver, 'deviceExists'>;
  address: BoundDevicesAddress;
  // False while the board is being edited: the overlay is empty then, and an empty overlay must
  // not strip the link out of the address the author will return to.
  enabled?: boolean;
}): BoundDevices {
  // The check that is read on the first render so a link never paints the board's defaults first.
  // The overlay, with the `bind.*` slice of the address it was last made to agree with. They move
  // together because the write-back below must only run while the address has not moved ahead of
  // the selection: an address that changed from outside is READ, never overwritten by a selection
  // that has not caught up with it yet.
  const [core, setCore] = useState<{ selection: Record<string, SlotBinding>; synced: string }>(() => ({
    selection: {},
    synced: bindParamsOf(address.search),
  }));
  const { selection } = core;
  const [phase, setPhase] = useState<BoundDevicesPhase>(() =>
    hasBindParams(address.search, definition) ? 'checking' : 'ready',
  );
  const [problems, setProblems] = useState<LinkProblems | null>(null);
  const [okBinds, setOkBinds] = useState<Record<string, string>>({});

  const definitionRef = useRef(definition);
  definitionRef.current = definition;
  const resolverRef = useRef(resolver);
  resolverRef.current = resolver;
  const replaceRef = useRef(address.replaceSearch);
  replaceRef.current = address.replaceSearch;

  // The `bind.*` slice of the address as this hook last wrote or last read it. An address whose
  // slice differs from this changed from OUTSIDE and is a new link.
  const seenRef = useRef<string | null>(null);
  const generation = useRef(0);
  const [attempt, setAttempt] = useState(0);

  const search = address.search;
  useEffect(() => {
    if (!enabled) return;
    const slice = bindParamsOf(search);
    // Our own write coming back round: nothing to read.
    if (seenRef.current === slice) return;
    seenRef.current = slice;

    const parsed = parseBindParams(search, definitionRef.current);
    if (Object.keys(parsed.binds).length === 0 && parsed.issues.length === 0) {
      generation.current += 1;
      setCore({ selection: {}, synced: slice });
      setProblems(null);
      setPhase('ready');
      return;
    }
    const gen = ++generation.current;
    setPhase('checking');
    void validateBinds(parsed.binds, resolverRef.current).then((check) => {
      if (gen !== generation.current) return;
      setOkBinds(check.ok);
      if (parsed.issues.length === 0 && check.missing.length === 0 && check.failed.length === 0) {
        setCore({ selection: bindsToSelection(check.ok), synced: slice });
        setProblems(null);
        setPhase('ready');
      } else {
        setProblems({ issues: parsed.issues, missing: check.missing, failed: check.failed });
        setCore((c) => ({ ...c, synced: slice }));
        setPhase('refused');
      }
    });
  }, [search, enabled, attempt]);

  // selection ─► address
  useEffect(() => {
    if (!enabled || phase !== 'ready') return;
    const current = bindParamsOf(search);
    if (current !== core.synced) return; // the address moved first: the read effect owns this turn
    const next = withBindParams(search, selectionToBinds(core.selection, definition));
    const nextSlice = bindParamsOf(next);
    if (nextSlice === current) return;
    seenRef.current = nextSlice;
    setCore((c) => ({ ...c, synced: nextSlice }));
    replaceRef.current(next);
  }, [core, definition, phase, enabled, search]);

  const select = useCallback((t: SelectionTarget) => {
    setCore((c) => ({ ...c, selection: { ...c.selection, [t.slot]: t.binding } }));
  }, []);
  const reset = useCallback(() => setCore((c) => ({ ...c, selection: {} })), []);

  const dismiss = useCallback(() => {
    // Keep what DID check out, drop what did not; the write-back effect then cleans the address.
    setCore((c) => ({ ...c, selection: bindsToSelection(okBinds) }));
    setProblems(null);
    setPhase('ready');
  }, [okBinds]);
  const retry = useCallback(() => {
    seenRef.current = null;
    setAttempt((n) => n + 1);
  }, []);

  return { selection, select, reset, phase, problems: phase === 'refused' ? (problems ?? NO_PROBLEMS) : null, dismiss, retry };
}
