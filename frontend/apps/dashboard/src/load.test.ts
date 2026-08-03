// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// This test reads the checked-in samples off disk, so it needs the Node typings.
/// <reference types="node" />

import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import { effectiveBindings, sameBinding } from '@devicechain/dashboards';
import { describe, expect, it } from 'vitest';

import { loadDashboard, MAX_PASTE_BYTES } from './load';

// ---- The samples gate ----------------------------------------------------------
//
// /dash is paste-only, so `frontend/testdata/dash-samples` exists to give someone
// something to paste. A sample that has quietly rotted is worse than none: every
// widget renders as an empty frame, which reads as a broken viewer rather than as a
// stale file. So the samples are checked, on both sides of the boundary they cross.
//
// 🔴 THIS HALF DOES NOT — AND CANNOT — CHECK THAT THE TOKENS NAME REAL ENTITIES.
// `wl-sensor-02` exists because the widgetlab manifest expands to it and assigns it to
// `wl-zone-02`; nothing on this side of the repo knows that. dash_sample_test.go in
// dc-simulator owns it, including the parent/child membership the scoped `sensor` slot
// depends on. What this half owns is the PASTE PATH: that the committed definition and
// these manifests, fed through the same loadDashboard the Render button calls, come
// back as a board bound to what the sample says it binds.

const TESTDATA = join(dirname(fileURLToPath(import.meta.url)), '..', '..', '..', 'testdata');
const SAMPLES = join(TESTDATA, 'dash-samples');
const FIXTURES = join(TESTDATA, 'sim-dashboards');

// Each sample directory is named for the dashboard token whose committed definition
// its manifests bind — so the pairing is structural rather than something a reader has
// to keep in step, and a renamed board leaves a directory that names no fixture.
function sampleBoards(): string[] {
  return readdirSync(SAMPLES, { withFileTypes: true })
    .filter((e) => e.isDirectory())
    .map((e) => e.name)
    .sort();
}

function sampleFiles(board: string): string[] {
  return readdirSync(join(SAMPLES, board))
    .filter((name) => name.endsWith('.json'))
    .sort();
}

const read = (path: string) => readFileSync(path, 'utf8');

describe('the checked-in /dash samples', () => {
  const boards = sampleBoards();

  // Without this the whole suite below is a loop over nothing, reported green. The
  // directory going missing (or losing its last board) is exactly how that happens.
  it('has samples at all', () => {
    expect(boards, `no sample directories under ${SAMPLES}`).not.toHaveLength(0);
  });

  describe.each(boards)('%s', (board) => {
    const files = sampleFiles(board);
    const fixture = join(FIXTURES, `${board}.json`);

    it('is named for a board whose definition is committed', () => {
      // A directory naming no fixture is a board that was renamed or removed; its
      // manifests bind slots nothing declares any more.
      expect(() => read(fixture), `${board}/ names no fixture at ${fixture}`).not.toThrow();
    });

    it('holds at least one manifest', () => {
      expect(files, `${board}/ has no .json manifests, so it covers nothing`).not.toHaveLength(0);
    });

    // The README's step 2: the definition alone, manifest box empty. This is the
    // first of the "one definition + two manifests" pair — the board's own defaults.
    it('pairs with a definition that loads on its own', () => {
      const result = loadDashboard(read(fixture), '');
      expect('error' in result ? result.error : null).toBeNull();
    });

    it.each(files)('%s loads and rebinds the board', (file) => {
      const definitionText = read(fixture);
      const result = loadDashboard(definitionText, read(join(SAMPLES, board, file)));
      if ('error' in result) throw new Error(`${board}/${file}: ${result.error}`);
      const { definition, manifest } = result.loaded;

      // Every slot the sample names must be one the board declares, AT THE KIND THE
      // SLOT IS. Both halves of that are load-bearing and neither is enforced anywhere
      // else on the paste path:
      //
      //  - An undeclared slot name survives on purpose — effectiveBindings merges the
      //    key in, and resolveContextBindings deliberately passes it through for a host
      //    whose manifest binds slots the definition does not declare. So the platform
      //    is right to keep it and a checked-in SAMPLE is still wrong to have one: no
      //    widget on this board references it, so the line binds nothing.
      //  - A binding of the wrong KIND is the likelier slip, because the two stanzas sit
      //    one above the other in the file. parseSlotBinding validates a binding's shape
      //    and knows nothing of the slot it lands on, so a wrong-kind binding parses
      //    perfectly. What happens next depends on the slot, and BOTH outcomes are bad:
      //    a SCOPED slot's binding is dropped by the cascade and its widgets render
      //    empty, while a ROOT slot's is passed straight through — the cascade
      //    type-checks only the selection overlay, not the manifest — so the board
      //    renders the wrong shape of entity's data instead of nothing at all.
      const slots = definition.slots ?? {};
      const names = Object.keys(slots);
      expect(names, `${board} declares no slots, so nothing here binds anything`).not.toHaveLength(0);
      for (const [name, binding] of Object.entries(manifest)) {
        expect(names, `${board}/${file} binds "${name}", which ${board} does not declare`).toContain(name);
        expect(
          binding.kind,
          `${board}/${file} binds "${name}" as ${binding.kind}, but ${board} declares it as ` +
            `${slots[name]?.type} — a scoped slot renders empty and a root slot renders the ` +
            'wrong entity, and neither is reported anywhere',
        ).toBe(slots[name]?.type);
      }

      // And it must actually CHANGE something. A sample that reproduces the board's own
      // default bindings loads perfectly and demonstrates the opposite of the point:
      // the whole reason to paste a manifest is that the same definition renders
      // against different entities.
      const defaults = effectiveBindings(definition);
      const overridden = effectiveBindings(definition, manifest);
      // sameBinding, not a token comparison: two anchors can name one target through
      // different relationships, which a token-string oracle would call unchanged.
      const changed = Object.keys(overridden).filter((slot) => !sameBinding(overridden[slot], defaults[slot]));
      expect(
        changed,
        `${board}/${file} binds exactly what the definition already defaults to, so pasting it ` +
          'shows nothing a reader could tell from pasting nothing',
      ).not.toHaveLength(0);
    });
  });
});

// ---- loadDashboard's own failure modes ------------------------------------------
//
// The samples above only ever exercise the success path. These are what a person
// pasting the WRONG thing hits, and every one of them is a returned message rather
// than a throw, because Load renders the string and has no error boundary of its own.

describe('loadDashboard', () => {
  const board = JSON.stringify({ schemaVersion: 1, title: 'T', widgets: [] });

  // Both cases land in one try/catch, so this is one test with two inputs rather than
  // two tests proving the same branch: bad JSON, and JSON the parser refuses (`widgets`
  // not being an array is one of the few shapes it throws on rather than degrading).
  it.each([
    ['not JSON', '{not json'],
    ['refused by the parser', JSON.stringify({ schemaVersion: 1, widgets: 'nope' })],
  ])('reports a definition %s', (_label, text) => {
    const result = loadDashboard(text, '');
    expect('error' in result && result.error).toMatch(/^Definition: /);
  });

  it('refuses a paste over the server cap without parsing it', () => {
    const result = loadDashboard('x'.repeat(MAX_PASTE_BYTES + 1), '');
    expect('error' in result && result.error).toContain('too large');
  });

  it('reports a manifest that is not JSON', () => {
    expect('error' in loadDashboard(board, '{nope')).toBe(true);
  });

  it('refuses a manifest that is not an object of slot → binding', () => {
    for (const text of ['[]', 'null', '"a string"', '7']) {
      const result = loadDashboard(board, text);
      expect('error' in result, `manifest ${text} was accepted`).toBe(true);
    }
  });

  // The one that matters most: parseBindingManifest DROPS a malformed entry and
  // returns the rest, so without the count a typo'd binding would render as an
  // unexplained empty widget instead of an error.
  it('reports entries the manifest parser dropped', () => {
    const result = loadDashboard(board, JSON.stringify({ zone: { kind: 'device' } }));
    expect('error' in result && result.error).toContain('"zone" was ignored');
  });

  it('accepts an empty manifest as no overrides', () => {
    for (const text of ['', '   ', '{}']) {
      const result = loadDashboard(board, text);
      if ('error' in result) throw new Error(`manifest ${JSON.stringify(text)}: ${result.error}`);
      expect(result.loaded.manifest).toEqual({});
    }
  });
});
