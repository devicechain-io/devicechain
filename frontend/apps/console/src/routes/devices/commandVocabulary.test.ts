// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, it, expect } from 'vitest';
import { draftOnlyCommandKeys } from './commandVocabulary';

const cmd = (commandKey: string) => ({ commandKey });

describe('draftOnlyCommandKeys', () => {
  it('names drafts that have never been published', () => {
    expect(draftOnlyCommandKeys([cmd('reboot')], [cmd('reboot'), cmd('calibrate')])).toEqual([
      'calibrate',
    ]);
  });

  it('does not report an edited draft of an already-published command', () => {
    // The author changed reboot's schema but has not republished. The published copy is
    // still selectable, so reboot is NOT withheld — reporting it would tell the author a
    // command is missing when it is right there in the picker.
    expect(draftOnlyCommandKeys([cmd('reboot')], [cmd('reboot')])).toEqual([]);
  });

  it('reports everything when nothing is published', () => {
    expect(draftOnlyCommandKeys([], [cmd('reboot'), cmd('calibrate')])).toEqual([
      'reboot',
      'calibrate',
    ]);
  });

  it('reports nothing when there are no drafts', () => {
    expect(draftOnlyCommandKeys([cmd('reboot')], [])).toEqual([]);
  });

  // The gate matches command keys case-sensitively, so a mis-cased draft is a DIFFERENT
  // command that genuinely is unpublished. Folding case here would hide it.
  it('treats a differently-cased key as its own command', () => {
    expect(draftOnlyCommandKeys([cmd('reboot')], [cmd('Reboot')])).toEqual(['Reboot']);
  });

  it('collapses duplicate draft keys and keeps authoring order', () => {
    expect(
      draftOnlyCommandKeys([], [cmd('calibrate'), cmd('reboot'), cmd('calibrate')]),
    ).toEqual(['calibrate', 'reboot']);
  });
});
