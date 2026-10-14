// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it, vi } from 'vitest';
import type { DeviceImportRow, DeviceImportRowResult } from '@/lib/api/device-management';
import {
  IMPORT_CHUNK_SIZE,
  ImportRunError,
  autoMap,
  buildRows,
  chunk,
  countOutcomes,
  failedRowsCsv,
  fieldsForMode,
  resultsCsv,
  runImport,
  stripSecrets,
  type FileRowOutcome,
} from '@/routes/devices/deviceImport';

describe('autoMap', () => {
  it('maps the canonical headers, case and punctuation aside', () => {
    expect(
      autoMap(['Token', 'deviceTypeToken', 'External ID', 'name', 'Description', 'metadata'], 'NONE'),
    ).toEqual({
      token: 0,
      deviceTypeToken: 1,
      externalId: 2,
      name: 3,
      description: 4,
      metadata: 5,
    });
  });

  it('reads the headers the console export writes, in English and Spanish', () => {
    // The export's "Type" column is the type NAME and must not feed deviceTypeToken.
    expect(autoMap(['Token', 'Name', 'Type', 'Type token', 'Description', 'Created'], 'NONE')).toEqual({
      token: 0,
      name: 1,
      deviceTypeToken: 3,
      description: 4,
    });
    expect(autoMap(['Token', 'Nombre', 'Token del tipo', 'Descripción'], 'NONE')).toEqual({
      token: 0,
      name: 1,
      deviceTypeToken: 2,
      description: 3,
    });
  });

  it('only maps credential columns in factory-credentials mode', () => {
    const header = ['token', 'credentialType', 'credentialId', 'credentialValue'];
    expect(autoMap(header, 'NONE')).toEqual({ token: 0 });
    expect(autoMap(header, 'FACTORY_CREDENTIALS')).toEqual({
      token: 0,
      credentialType: 1,
      credentialId: 2,
      credentialValue: 3,
    });
    expect(fieldsForMode('NONE')).not.toContain('credentialValue');
  });
});

describe('buildRows', () => {
  const table = [
    ['d1', ' Pump ', 'gateway', '', '{"a":1}'],
    ['d2', '', '', 'EXT-2', ''],
  ];
  const mapping = { token: 0, name: 1, deviceTypeToken: 2, externalId: 3, metadata: 4 };

  it('trims cells, omits empty ones and lets the file-wide type fill a blank type cell', () => {
    expect(buildRows(table, mapping, 'NONE', 'sensor')).toEqual([
      { token: 'd1', deviceTypeToken: 'gateway', name: 'Pump', metadata: '{"a":1}' },
      { token: 'd2', deviceTypeToken: 'sensor', externalId: 'EXT-2' },
    ]);
  });

  it('sends the server a blank rather than guessing when a required value is missing', () => {
    expect(buildRows([['']], { token: 0 }, 'NONE')[0]).toEqual({ token: '', deviceTypeToken: '' });
  });

  it('sends credentials only in factory mode, and the secret verbatim', () => {
    const t = [['d1', 'mqtt_basic', ' u1 ', '  s3cret  ']];
    const m = { token: 0, credentialType: 1, credentialId: 2, credentialValue: 3 };
    expect(buildRows(t, m, 'NONE', 'sensor')[0]).toEqual({ token: 'd1', deviceTypeToken: 'sensor' });
    expect(buildRows(t, m, 'FACTORY_CREDENTIALS', 'sensor')[0]).toEqual({
      token: 'd1',
      deviceTypeToken: 'sensor',
      credentialType: 'MQTT_BASIC',
      credentialId: 'u1',
      credentialValue: '  s3cret  ',
    });
  });
});

describe('chunk', () => {
  it('splits at the per-call bound', () => {
    const parts = chunk(Array.from({ length: IMPORT_CHUNK_SIZE * 2 + 1 }, (_, i) => i));
    expect(parts.map((p) => p.length)).toEqual([IMPORT_CHUNK_SIZE, IMPORT_CHUNK_SIZE, 1]);
    expect(chunk([])).toEqual([]);
  });
});

const rowsOf = (n: number): DeviceImportRow[] =>
  Array.from({ length: n }, (_, i) => ({ token: `d${i + 1}`, deviceTypeToken: 't' }));

const echo = () =>
  vi.fn(
    async (rows: DeviceImportRow[], _dryRun: boolean) =>
      rows.map((r, i) => ({ row: i + 1, token: r.token, outcome: 'CREATED' })) as DeviceImportRowResult[],
  );

describe('runImport', () => {
  it('sends sequential chunks and keys every outcome to its file row', async () => {
    const send = echo();
    const progress: number[] = [];
    const { outcomes, stopped } = await runImport(rowsOf(2500), false, send, {
      onProgress: (n) => progress.push(n),
    });
    expect(stopped).toBe(false);
    expect(send.mock.calls.map((c) => c[0].length)).toEqual([1000, 1000, 500]);
    expect(outcomes).toHaveLength(2500);
    expect(outcomes[1000]).toMatchObject({ row: 1001, token: 'd1001' });
    expect(outcomes[2499].row).toBe(2500);
    expect(progress).toEqual([1000, 2000, 2500]);
  });

  it('passes dryRun through', async () => {
    const send = echo();
    await runImport(rowsOf(1), true, send);
    expect(send.mock.calls[0][1]).toBe(true);
  });

  it('stops before the next chunk when aborted and keeps what finished', async () => {
    const ctl = new AbortController();
    const send = vi.fn(async (rows: DeviceImportRow[], _dryRun: boolean) => {
      ctl.abort();
      return rows.map((r, i) => ({ row: i + 1, token: r.token, outcome: 'CREATED' as const }));
    });
    const { outcomes, stopped } = await runImport(rowsOf(2500), false, send, { signal: ctl.signal });
    expect(stopped).toBe(true);
    expect(send).toHaveBeenCalledTimes(1);
    expect(outcomes).toHaveLength(1000);
  });

  it('surfaces a failed chunk with the outcomes already gathered', async () => {
    let call = 0;
    const send = vi.fn(async (rows: DeviceImportRow[], _dryRun: boolean) => {
      if (++call === 2) throw new Error('network down');
      return rows.map((r, i) => ({ row: i + 1, token: r.token, outcome: 'CREATED' as const }));
    });
    const err = await runImport(rowsOf(1500), false, send).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ImportRunError);
    expect((err as ImportRunError).outcomes).toHaveLength(1000);
    expect((err as ImportRunError).message).toBe('network down');
  });
});

describe('results and secrets', () => {
  const outcomes: FileRowOutcome[] = [
    { row: 1, token: 'd1', outcome: 'CREATED' },
    { row: 2, token: 'd2', outcome: 'EXISTS' },
    { row: 3, token: 'd3', outcome: 'CONFLICT', code: 'CONFLICT', message: 'name differs' },
    { row: 4, token: 'd4', outcome: 'INVALID', code: 'NOT_FOUND', message: '=evil' },
  ];

  it('counts outcomes', () => {
    expect(countOutcomes(outcomes)).toEqual({ CREATED: 1, EXISTS: 1, CONFLICT: 1, INVALID: 1, REFUSED: 0 });
  });

  it('writes a results file that is formula-guarded', () => {
    const csv = resultsCsv(outcomes);
    expect(csv.split('\r\n')[0]).toBe('row,token,outcome,code,message');
    expect(csv).toContain("'=evil");
  });

  it('writes failed rows under the canonical headers and never the secret column', () => {
    const table = [
      ['d1', 'mqtt_basic', 'u1', 'TOP-SECRET-1'],
      ['d2', 'mqtt_basic', 'u2', 'TOP-SECRET-2'],
      ['d3', 'mqtt_basic', 'u3', 'TOP-SECRET-3'],
      ['d4', 'mqtt_basic', 'u4', 'TOP-SECRET-4'],
    ];
    const mapping = { token: 0, credentialType: 1, credentialId: 2, credentialValue: 3 };
    const csv = failedRowsCsv(table, mapping, 'FACTORY_CREDENTIALS', outcomes, 'sensor');
    expect(csv).not.toContain('TOP-SECRET');
    expect(csv).not.toContain('credentialValue');
    const lines = csv.trimEnd().split('\r\n');
    expect(lines[0]).toBe(
      'token,deviceTypeToken,externalId,name,description,metadata,credentialType,credentialId,outcome,code,message',
    );
    expect(lines).toHaveLength(3); // header + the two failed rows
    expect(lines[1].startsWith('d3,sensor,')).toBe(true);
  });

  it('blanks the secret column once the import is over', () => {
    const t = [['d1', 'TOP-SECRET']];
    expect(stripSecrets(t, { token: 0, credentialValue: 1 })).toEqual([['d1', '']]);
    expect(stripSecrets(t, { token: 0 })).toBe(t);
  });
});
