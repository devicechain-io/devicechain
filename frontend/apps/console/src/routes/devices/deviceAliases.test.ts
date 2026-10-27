// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import {
  aliasInputs,
  aliasProblems,
  aliasesChanged,
  aliasesValid,
  newDeviceLink,
  rowFromParam,
  rowOf,
  rowScheme,
  rowsOf,
  type AliasRow,
} from './deviceAliases';

const deveui = (value: string): AliasRow => ({ kind: 'lorawan.deveui', customName: '', value });

describe('alias rows', () => {
  it('maps a stored alias to a row and back to the scheme', () => {
    expect(rowOf('lorawan.deveui', '70B3D57ED0012345')).toEqual(deveui('70B3D57ED0012345'));
    const custom = rowOf('custom.serial', 'S-1');
    expect(custom).toEqual({ kind: 'custom', customName: 'serial', value: 'S-1' });
    expect(rowScheme(custom)).toBe('custom.serial');
    expect(rowScheme(rowOf('tts.device-id', 'x'))).toBe('tts.device-id');
    expect(rowsOf(null)).toEqual([]);
  });

  it('turns rows into the input the server takes, dropping blank rows and trimming', () => {
    const rows: AliasRow[] = [deveui(' 70b3d57ed0012345 '), { kind: 'custom', customName: '', value: '' }, rowOf('custom.serial', 'S-1')];
    expect(aliasInputs(rows)).toEqual([
      { scheme: 'lorawan.deveui', value: '70b3d57ed0012345' },
      { scheme: 'custom.serial', value: 'S-1' },
    ]);
  });
});

describe('per-scheme validation', () => {
  it('a DevEUI is exactly 16 hex digits, either case', () => {
    expect(aliasProblems([deveui('70b3d57ed0012345')])).toEqual([null]);
    expect(aliasProblems([deveui('70B3D57ED001234')])).toEqual(['value']);
    expect(aliasProblems([deveui('70B3D57ED00123G5')])).toEqual(['value']);
    expect(aliasProblems([deveui('70-B3-D5-7E-D0-01-23-45')])).toEqual(['value']);
  });

  it('a network-server device id is 1 to 36 letters, digits and inner hyphens', () => {
    const tts = (value: string): AliasRow => ({ kind: 'tts.device-id', customName: '', value });
    expect(aliasProblems([tts('My-Sensor-01')])).toEqual([null]);
    expect(aliasProblems([tts('-bad')])).toEqual(['value']);
    expect(aliasProblems([tts('a'.repeat(37))])).toEqual(['value']);
  });

  it('a custom scheme needs a well-formed name and a printable value', () => {
    const custom = (customName: string, value: string): AliasRow => ({ kind: 'custom', customName, value });
    expect(aliasProblems([custom('serial', 'AbC')])).toEqual([null]);
    expect(aliasProblems([custom('Serial', 'x')])).toEqual(['name']);
    expect(aliasProblems([custom('serial', 'a\u0007b')])).toEqual(['value']);
    expect(aliasProblems([custom('serial', 'x'.repeat(129))])).toEqual(['value']);
  });

  it('a second row for a scheme is a duplicate, and an untouched row is not a problem', () => {
    const blank: AliasRow = { kind: 'lorawan.deveui', customName: '', value: '' };
    expect(aliasProblems([deveui('70B3D57ED0012345'), deveui('70B3D57ED0012346')])).toEqual([null, 'duplicate']);
    expect(aliasProblems([blank])).toEqual([null]);
    expect(aliasesValid([deveui('70B3D57ED0012345'), blank])).toBe(true);
    expect(aliasesValid([deveui('nope')])).toBe(false);
  });
});

describe('what counts as a change', () => {
  it('the same alias in another spelling or order is not a change', () => {
    const a = [deveui('70B3D57ED0012345'), rowOf('custom.serial', 'S-1')];
    expect(aliasesChanged(a, [rowOf('custom.serial', 'S-1'), deveui('70b3d57ed0012345')])).toBe(false);
  });

  it('a different value, an added or a removed alias is a change', () => {
    const a = [deveui('70B3D57ED0012345')];
    expect(aliasesChanged(a, [deveui('70B3D57ED0012346')])).toBe(true);
    expect(aliasesChanged(a, [...a, rowOf('custom.serial', 'x')])).toBe(true);
    expect(aliasesChanged(a, [])).toBe(true);
  });

  it('a custom value keeps its case', () => {
    expect(aliasesChanged([rowOf('custom.serial', 'abc')], [rowOf('custom.serial', 'ABC')])).toBe(true);
  });
});

describe('the link from an unregistered device', () => {
  it('round-trips the alias (and the only type) through the new-device link', () => {
    const link = newDeviceLink('lorawan.deveui', '70B3D57ED0012345', 'meter');
    const params = new URL(link, 'http://x').searchParams;
    expect(rowFromParam(params.get('newAlias'))).toEqual(deveui('70B3D57ED0012345'));
    expect(params.get('newType')).toBe('meter');
  });

  it('keeps a value that itself contains the separator', () => {
    const params = new URL(newDeviceLink('custom.serial', 'a|b'), 'http://x').searchParams;
    expect(rowFromParam(params.get('newAlias'))).toEqual(rowOf('custom.serial', 'a|b'));
    expect(params.has('newType')).toBe(false);
  });

  it('ignores a malformed parameter', () => {
    expect(rowFromParam(null)).toBeNull();
    expect(rowFromParam('nobar')).toBeNull();
    expect(rowFromParam('|x')).toBeNull();
  });
});
