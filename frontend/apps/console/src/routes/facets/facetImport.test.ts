// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it, vi } from 'vitest';
import type { FacetValueImportRow, FacetValueImportRowResult } from '@/lib/api/facet-import';
import {
  DUPLICATE_CODE,
  NO_RESULT_CODE,
  autoMap,
  buildRows,
  countOutcomes,
  failedRowsCsv,
  findDuplicates,
  mergeOutcomes,
  resultsCsv,
  runFacetImport,
  stripSecretColumns,
  ImportRunError,
  type FileRowOutcome,
} from './facetImport';

const row = (token: string, key = 'site', value = 'v', memberType = 'device'): FacetValueImportRow => ({
  memberType,
  token,
  key,
  value,
});

// Answers every row SET, the way the server does for a fresh value, keyed to the call.
const answerAll =
  (outcome: FacetValueImportRowResult['outcome'] = 'SET') =>
  async (rows: FacetValueImportRow[]): Promise<FacetValueImportRowResult[]> =>
    rows.map((r, i) => ({ row: i + 1, memberType: r.memberType, token: r.token, key: r.key, outcome }));

describe('autoMap', () => {
  it('finds the four columns by their own names and common spellings', () => {
    expect(autoMap(['memberType', 'token', 'key', 'value'])).toEqual({ memberType: 0, token: 1, key: 2, value: 3 });
    expect(autoMap(['Entity type', 'Entity token', 'Facet key', 'Facet value'])).toEqual({
      memberType: 0,
      token: 1,
      key: 2,
      value: 3,
    });
  });

  it('leaves an unrecognised column unmapped and never maps one column twice', () => {
    expect(autoMap(['serial', 'token', 'value'])).toEqual({ token: 1, value: 2 });
    expect(autoMap(['token', 'token'])).toEqual({ token: 0 });
  });
});

describe('buildRows', () => {
  it('uses the cell over the file default, lowercases the family and trims identifiers', () => {
    const table = [
      [' Device ', ' d1 ', ' site ', ' north '],
      ['', 'a1', '', 'x'],
    ];
    expect(buildRows(table, { memberType: 0, token: 1, key: 2, value: 3 }, { memberType: 'asset', key: 'zone' })).toEqual([
      { memberType: 'device', token: 'd1', key: 'site', value: ' north ' },
      { memberType: 'asset', token: 'a1', key: 'zone', value: 'x' },
    ]);
  });

  it('sends the value as written, and an unmapped value as empty so the server refuses it', () => {
    expect(buildRows([['d1', '   ']], { token: 0, value: 1 }, { memberType: 'device', key: 'k' })[0].value).toBe('   ');
    expect(buildRows([['d1']], { token: 0 }, { memberType: 'device', key: 'k' })[0].value).toBe('');
  });

  it('undoes the export formula guard on a value', () => {
    expect(buildRows([["d1", "'=SUM(1)"]], { token: 0, value: 1 }, { memberType: 'device', key: 'k' })[0].value).toBe('=SUM(1)');
  });
});

describe('findDuplicates', () => {
  it('reports later occurrences of (type, token, key), never the first', () => {
    const rows = [row('d1'), row('d2'), row('d1'), row('d1', 'zone'), row('d1', 'site', 'other'), row('d1', 'site', 'v', 'asset')];
    expect([...findDuplicates(rows)]).toEqual([3, 5]);
  });

  it('does not compare rows that lack a token or a key', () => {
    expect(findDuplicates([row(''), row('')]).size).toBe(0);
    expect(findDuplicates([row('d1', ''), row('d1', '')]).size).toBe(0);
  });
});

describe('runFacetImport', () => {
  it('sends sequential chunks of at most 1000 and folds results onto file rows', async () => {
    const rows = Array.from({ length: 2500 }, (_, i) => row(`d${i}`));
    let inFlight = 0;
    let maxInFlight = 0;
    const sizes: number[] = [];
    const send = vi.fn(async (part: FacetValueImportRow[], dry: boolean) => {
      inFlight++;
      maxInFlight = Math.max(maxInFlight, inFlight);
      sizes.push(part.length);
      await Promise.resolve();
      inFlight--;
      return answerAll()(part).then((r) => (dry ? r : r));
    });
    const { outcomes, stopped } = await runFacetImport(rows, true, send);
    expect(stopped).toBe(false);
    expect(sizes).toEqual([1000, 1000, 500]);
    expect(maxInFlight).toBe(1);
    expect(outcomes).toHaveLength(2500);
    expect(outcomes[0]).toMatchObject({ row: 1, token: 'd0' });
    expect(outcomes[2499]).toMatchObject({ row: 2500, token: 'd2499' });
    expect(send.mock.calls.every((c) => c[1] === true)).toBe(true);
  });

  it('holds back a whole-file duplicate, even across chunks, and answers it locally', async () => {
    const rows = [row('d1'), ...Array.from({ length: 1000 }, (_, i) => row(`x${i}`)), row('d1')];
    const sent: FacetValueImportRow[][] = [];
    const { outcomes } = await runFacetImport(rows, false, async (part) => {
      sent.push(part);
      return answerAll()(part);
    });
    expect(sent.flat()).toHaveLength(1001);
    expect(sent.flat().filter((r) => r.token === 'd1')).toHaveLength(1);
    const last = outcomes[outcomes.length - 1];
    expect(last).toMatchObject({ row: 1002, outcome: 'INVALID', code: DUPLICATE_CODE });
    expect(outcomes[0].outcome).toBe('SET');
  });

  it('turns an unanswered row into a REFUSED NO_RESULT, never a success', async () => {
    const { outcomes } = await runFacetImport([row('d1'), row('d2')], false, async (part) => [
      { row: 1, memberType: 'device', token: part[0].token, key: 'site', outcome: 'SET' },
    ]);
    expect(outcomes[1]).toMatchObject({ row: 2, outcome: 'REFUSED', code: NO_RESULT_CODE });
  });

  it('stops before the next chunk when aborted and keeps what was answered', async () => {
    const controller = new AbortController();
    const rows = Array.from({ length: 1500 }, (_, i) => row(`d${i}`));
    const send = vi.fn(async (part: FacetValueImportRow[]) => {
      controller.abort();
      return answerAll()(part);
    });
    const { outcomes, stopped } = await runFacetImport(rows, false, send, { signal: controller.signal });
    expect(stopped).toBe(true);
    expect(send).toHaveBeenCalledTimes(1);
    expect(outcomes).toHaveLength(1000);
  });

  it('attaches earlier answers to a chunk failure', async () => {
    const rows = Array.from({ length: 1500 }, (_, i) => row(`d${i}`));
    let calls = 0;
    const err = await runFacetImport(rows, false, async (part) => {
      if (++calls === 2) throw new Error('network down');
      return answerAll()(part);
    }).catch((e) => e);
    expect(err).toBeInstanceOf(ImportRunError);
    expect((err as ImportRunError).outcomes).toHaveLength(1000);
    expect((err as ImportRunError).message).toBe('network down');
  });

  it('retries only the named rows and still judges duplicates against the whole file', async () => {
    const rows = [row('d1'), row('d2'), row('d1'), row('d3')];
    const sent: string[] = [];
    const { outcomes } = await runFacetImport(
      rows,
      false,
      async (part) => {
        sent.push(...part.map((p) => p.token));
        return answerAll()(part);
      },
      { only: new Set([2, 3]) },
    );
    expect(sent).toEqual(['d2']);
    expect(outcomes.map((o) => [o.row, o.outcome])).toEqual([
      [2, 'SET'],
      [3, 'INVALID'],
    ]);
  });

  it('reports progress as rows are answered', async () => {
    const seen: number[] = [];
    await runFacetImport(Array.from({ length: 1200 }, (_, i) => row(`d${i}`)), true, answerAll(), {
      onProgress: (n) => seen.push(n),
    });
    expect(seen).toEqual([0, 1000, 1200]);
  });
});

describe('outcomes', () => {
  const o = (r: number, outcome: FileRowOutcome['outcome']): FileRowOutcome => ({
    row: r,
    memberType: 'device',
    token: `d${r}`,
    key: 'site',
    outcome,
  });

  it('counts by outcome', () => {
    expect(countOutcomes([o(1, 'SET'), o(2, 'SET'), o(3, 'REFUSED')])).toEqual({ SET: 2, EXISTS: 0, INVALID: 0, REFUSED: 1 });
  });

  it('merges a retry over earlier answers in file order', () => {
    const merged = mergeOutcomes([o(1, 'SET'), o(2, 'REFUSED'), o(3, 'INVALID')], [o(2, 'SET')]);
    expect(merged.map((m) => [m.row, m.outcome])).toEqual([
      [1, 'SET'],
      [2, 'SET'],
      [3, 'INVALID'],
    ]);
  });

  it('writes the results without any value', () => {
    expect(resultsCsv([{ ...o(1, 'INVALID'), code: 'NOT_FOUND', message: 'facet key is not declared' }])).toBe(
      'row,memberType,token,key,outcome,code,message\r\n1,device,d1,site,INVALID,NOT_FOUND,facet key is not declared\r\n',
    );
  });
});

describe('failed-rows file', () => {
  const header = ['memberType', 'token', 'key', 'value', 'api_key', 'Password'];
  const table = [
    ['device', 'd1', 'site', 'north', 'AKIA-SECRET-1', 'hunter2'],
    ['device', 'd2', 'site', 'south', 'AKIA-SECRET-2', 'hunter3'],
  ];
  const mapping = autoMap(header);
  const outcomes: FileRowOutcome[] = [
    { row: 1, memberType: 'device', token: 'd1', key: 'site', outcome: 'SET' },
    { row: 2, memberType: 'device', token: 'd2', key: 'site', outcome: 'INVALID', code: 'NOT_FOUND', message: 'no such entity' },
  ];

  it('holds only the failed rows in the canonical columns and never a secret column', () => {
    const csv = failedRowsCsv(table, header, mapping, {}, outcomes);
    expect(csv).toBe(
      'memberType,token,key,value,outcome,code,message\r\ndevice,d2,site,south,INVALID,NOT_FOUND,no such entity\r\n',
    );
    expect(csv).not.toContain('AKIA');
    expect(csv).not.toContain('hunter');
  });

  it('blanks the value when the value column itself is a secret', () => {
    const h = ['memberType', 'token', 'key', 'Client Secret'];
    const csv = failedRowsCsv([['device', 'd2', 'site', 'topsecret']], h, autoMap(h), {}, [outcomes[1]]);
    expect(csv).not.toContain('topsecret');
  });

  it('writes the file defaults into the rows that rely on them', () => {
    const csv = failedRowsCsv([['d2', 'south']], ['token', 'value'], { token: 0, value: 1 }, { memberType: 'area', key: 'zone' }, [
      { row: 1, memberType: 'area', token: 'd2', key: 'zone', outcome: 'REFUSED', code: 'IMPORT_FAILED', message: 'again' },
    ]);
    expect(csv).toContain('area,d2,zone,south,REFUSED');
  });

  it('strips secret-looking columns from the held table but keeps the header and the rest', () => {
    const stripped = stripSecretColumns([header, ...table]);
    expect(stripped[0]).toEqual(header);
    expect(stripped[1]).toEqual(['device', 'd1', 'site', 'north', '', '']);
    expect(JSON.stringify(stripped)).not.toContain('hunter');
  });
});
