// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, it, expect } from 'vitest';
import { commandChoices, draftOnlyCommandKeys } from './commandVocabulary';

const cmd = (commandKey: string) => ({ commandKey });

describe('commandChoices', () => {
  // The regression this pins: reading only `commands` and ignoring `constrained` makes
  // an unconstrained device look like a device with nothing to send. It has the opposite
  // meaning — the gate accepts anything — so the drafts must stay offerable.
  it('offers drafts when the profile constrains nothing', () => {
    const choices = commandChoices({ constrained: false, commands: [] }, [
      cmd('reboot'),
      cmd('calibrate'),
    ]);
    expect(choices.constrained).toBe(false);
    expect(choices.selectable.map((c) => c.commandKey)).toEqual(['reboot', 'calibrate']);
    // Nothing is withheld, so nothing is reported as withheld.
    expect(choices.draftOnly).toEqual([]);
  });

  it('offers only published commands when the profile constrains', () => {
    const choices = commandChoices({ constrained: true, commands: [cmd('reboot')] }, [
      cmd('reboot'),
      cmd('calibrate'),
    ]);
    expect(choices.constrained).toBe(true);
    expect(choices.selectable.map((c) => c.commandKey)).toEqual(['reboot']);
    expect(choices.draftOnly).toEqual(['calibrate']);
  });

  // A dashboard outlives the device it points at. The panel must stay usable so the
  // author can re-point it, rather than collapsing into an error.
  it('treats a missing device as unconstrained rather than failing', () => {
    expect(commandChoices(null, [cmd('reboot')])).toEqual({
      selectable: [cmd('reboot')],
      draftOnly: [],
      constrained: false,
    });
    expect(commandChoices(undefined, []).constrained).toBe(false);
  });

  it('reports a constrained profile that publishes nothing as having nothing selectable', () => {
    // Not reachable from the server today — constrained is derived as "has published
    // commands" — but the picker must not offer drafts if it ever becomes reachable,
    // because a constrained gate would reject them.
    const choices = commandChoices({ constrained: true, commands: [] }, [cmd('reboot')]);
    expect(choices.selectable).toEqual([]);
    expect(choices.draftOnly).toEqual(['reboot']);
  });
});

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
