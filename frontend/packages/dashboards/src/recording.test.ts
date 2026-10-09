// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import {
  parseBoardRecording,
  recordedWallTimeMs,
  recordingBounds,
  RecordingFormatError,
  simulationScaleAt,
} from './recording';
import { FIXTURE_START_MS, fixtureJson } from './testing/recording-fixture';

type Json = Record<string, any>;

// Parse a corrupted copy of the fixture and return the error it is refused with.
function refusal(mutate: (doc: Json) => void): RecordingFormatError {
  const doc = JSON.parse(JSON.stringify(fixtureJson())) as Json;
  mutate(doc);
  try {
    parseBoardRecording(doc);
  } catch (err) {
    expect(err).toBeInstanceOf(RecordingFormatError);
    return err as RecordingFormatError;
  }
  throw new Error('the corrupted recording was accepted');
}

describe('parseBoardRecording', () => {
  it('accepts the fixture, including after a trip through JSON text', () => {
    const rec = parseBoardRecording(JSON.parse(JSON.stringify(fixtureJson())));
    expect(rec.devices.map((d) => d.token)).toEqual(['sp-hauler-01', 'sp-hauler-02', 'sp-plant-01']);
    expect(rec.channels).toEqual({
      measurements: ['fuel_pct', 'tyre_pressure_kpa', 'throughput_tph'],
      alarms: true,
      locations: false,
      commands: false,
    });
    expect(rec.excerpt).toBeUndefined();
  });

  it('accepts an excerpt, and keeps its window', () => {
    const doc = fixtureJson();
    doc.excerpt = { fromMs: 0, toMs: 50_000 };
    expect(parseBoardRecording(doc).excerpt).toEqual({ fromMs: 0, toMs: 50_000 });
  });

  it('refuses anything that is not an object', () => {
    for (const bad of [null, [], 'x', 3, undefined]) {
      expect(() => parseBoardRecording(bad)).toThrow(RecordingFormatError);
    }
  });

  it('refuses an unknown key at the top level and at every nesting depth', () => {
    expect(refusal((d) => (d.extra = 1)).path).toBe('$.extra');
    expect(refusal((d) => (d.build.extra = 1)).path).toBe('$.build.extra');
    expect(refusal((d) => (d.channels.extra = true)).path).toBe('$.channels.extra');
    expect(refusal((d) => (d.clock[0].extra = 1)).path).toBe('$.clock[0].extra');
    expect(refusal((d) => (d.devices[0].extra = 1)).path).toBe('$.devices[0].extra');
    expect(refusal((d) => (d.board.extra = 1)).path).toBe('$.board.extra');
    expect(refusal((d) => (d.converter.extra = 1)).path).toBe('$.converter.extra');
    expect(refusal((d) => (d.chapters[0].extra = 1)).path).toBe('$.chapters[0].extra');
    expect(refusal((d) => (d.measurements.extra = [])).path).toBe('$.measurements.extra');
    expect(refusal((d) => (d.alarms.extra = [])).path).toBe('$.alarms.extra');
    expect(refusal((d) => (d.alarms.snapshots[0].extra = 1)).path).toBe('$.alarms.snapshots[0].extra');
    expect(refusal((d) => (d.alarms.events[0].extra = 1)).path).toBe('$.alarms.events[0].extra');
    expect(refusal((d) => (d.excerpt = { fromMs: 0, toMs: 1, extra: 1 })).path).toBe('$.excerpt.extra');
  });

  it('refuses a missing required key', () => {
    expect(refusal((d) => delete d.runId).path).toBe('$.runId');
    expect(refusal((d) => delete d.measurements.s).path).toBe('$.measurements.s');
    expect(refusal((d) => delete d.alarms.events[0].sev).path).toBe('$.alarms.events[0].sev');
  });

  it('refuses a wrong kind and an unsupported or absent format version', () => {
    expect(refusal((d) => (d.kind = 'something-else')).path).toBe('$.kind');
    expect(refusal((d) => (d.formatVersion = 2)).path).toBe('$.formatVersion');
    expect(refusal((d) => (d.formatVersion = '1')).path).toBe('$.formatVersion');
  });

  it('refuses ragged measurement columns', () => {
    expect(refusal((d) => d.measurements.v.pop()).path).toBe('$.measurements.v');
    expect(refusal((d) => d.measurements.d.push(0)).path).toBe('$.measurements.d');
    expect(refusal((d) => d.measurements.s.pop()).path).toBe('$.measurements.s');
  });

  it('refuses a time column that decreases or leaves the run', () => {
    expect(refusal((d) => (d.measurements.t[5] = 1)).path).toBe('$.measurements.t[5]');
    expect(refusal((d) => (d.measurements.t[16] = d.durationMs + 1)).path).toBe('$.measurements.t[16]');
    expect(refusal((d) => (d.measurements.t[0] = -1)).path).toBe('$.measurements.t[0]');
  });

  it('holds an excerpt to its own window', () => {
    expect(refusal((d) => (d.excerpt = { fromMs: 5_000, toMs: 50_000 })).path).toBe('$.measurements.t[0]');
    expect(refusal((d) => (d.excerpt = { fromMs: 0, toMs: 20_000 })).path).toContain('$.measurements.t');
    expect(refusal((d) => (d.excerpt = { fromMs: 10, toMs: 10 })).path).toBe('$.excerpt');
    expect(refusal((d) => (d.excerpt = { fromMs: 0, toMs: 61_000 })).path).toBe('$.excerpt');
  });

  it('refuses an index outside the device table or the metric list', () => {
    expect(refusal((d) => (d.measurements.d[0] = 3)).path).toBe('$.measurements.d[0]');
    expect(refusal((d) => (d.measurements.n[0] = -1)).path).toBe('$.measurements.n[0]');
    expect(refusal((d) => (d.measurements.n[0] = 1.5)).path).toBe('$.measurements.n[0]');
    expect(refusal((d) => (d.measurements.n[0] = 3)).path).toBe('$.measurements.n[0]');
  });

  it('refuses values of the wrong type, including non-finite numbers and a bad seed flag', () => {
    expect(refusal((d) => (d.measurements.v[0] = '90')).path).toBe('$.measurements.v[0]');
    expect(refusal((d) => (d.measurements.v[0] = null)).path).toBe('$.measurements.v[0]');
    expect(refusal((d) => (d.measurements.s[0] = 2)).path).toBe('$.measurements.s[0]');
    expect(refusal((d) => (d.durationMs = 'long')).path).toBe('$.durationMs');
  });

  it('refuses duplicate devices and duplicate metric names', () => {
    expect(refusal((d) => (d.devices[1].token = d.devices[0].token)).path).toBe('$.devices');
    expect(refusal((d) => (d.devices[1].id = d.devices[0].id)).path).toBe('$.devices');
    expect(refusal((d) => (d.channels.measurements[1] = d.channels.measurements[0])).path).toBe('$.channels.measurements');
  });

  it('refuses a channel claim this version cannot honour', () => {
    expect(refusal((d) => (d.channels.commands = true)).path).toBe('$.channels.commands');
    expect(refusal((d) => (d.channels.locations = true)).path).toBe('$.channels.locations');
    expect(refusal((d) => d.locations.push({})).path).toBe('$.locations');
    expect(refusal((d) => (d.locations = {})).path).toBe('$.locations');
    expect(refusal((d) => (d.channels.alarms = false)).path).toBe('$.alarms');
  });

  it('refuses alarm records that are malformed', () => {
    expect(refusal((d) => (d.alarms.events[0].id = 'b1c2d3e4-platform-uuid')).path).toBe('$.alarms.events[0].id');
    expect(refusal((d) => (d.alarms.events[0].dev = 'nobody')).path).toBe('$.alarms.events[0].dev');
    expect(refusal((d) => (d.alarms.events[0].state = 'ACKED')).path).toBe('$.alarms.events[0].state');
    expect(refusal((d) => (d.alarms.events[0].sev = 'SEVERE')).path).toBe('$.alarms.events[0].sev');
    expect(refusal((d) => (d.alarms.events[0].occ = 'yesterday')).path).toBe('$.alarms.events[0].occ');
    expect(refusal((d) => (d.alarms.events[3].ack = false)).path).toBe('$.alarms.events[3].ack');
    expect(refusal((d) => (d.alarms.events[1].tMs = 1)).path).toBe('$.alarms.events[1].tMs');
    expect(refusal((d) => (d.alarms.events[0].tMs = 61_000)).path).toBe('$.alarms.events[0].tMs');
    expect(refusal((d) => (d.alarms.snapshots[0].total = -1)).path).toBe('$.alarms.snapshots[0].total');
  });

  it('refuses a snapshot whose total is smaller than the alarms it lists', () => {
    expect(
      refusal((d) => {
        d.alarms.snapshots[0].alarms = [{ ...d.alarms.events[0] }];
        delete d.alarms.snapshots[0].alarms[0].tMs;
      }).path,
    ).toBe('$.alarms.snapshots[0].total');
  });

  it('refuses a malformed identity: hashes, commits, start instant', () => {
    expect(refusal((d) => (d.board.sha256 = 'xyz')).path).toBe('$.board.sha256');
    expect(refusal((d) => (d.board.sourceCommit = 'abc123')).path).toBe('$.board.sourceCommit');
    expect(refusal((d) => (d.sourceHashes['run.json'] = 'A'.repeat(64))).path).toBe('$.sourceHashes.run.json');
    expect(refusal((d) => (d.build.gitSha = 'not-a-sha')).path).toBe('$.build.gitSha');
    expect(refusal((d) => (d.startedAtUtc = '2026-01-02 03:04:05')).path).toBe('$.startedAtUtc');
    expect(refusal((d) => (d.startedAtUtc = '2026-13-45T99:00:00Z')).path).toBe('$.startedAtUtc');
  });

  it('refuses a source-hash key that would rewrite the prototype', () => {
    const doc = fixtureJson();
    doc.sourceHashes = JSON.parse(`{"__proto__":"${'a'.repeat(64)}"}`);
    expect(() => parseBoardRecording(doc)).toThrow(/reserved key/);
  });

  it('refuses a clock that does not increase or has a non-positive scale', () => {
    expect(refusal((d) => d.clock.push({ fromMs: 11, mode: 'real', scale: 2 })).path).toBe('$.clock[1].fromMs');
    expect(refusal((d) => (d.clock[0].scale = 0)).path).toBe('$.clock[0].scale');
    expect(refusal((d) => (d.clock[0].mode = '')).path).toBe('$.clock[0].mode');
  });

  it('refuses a chapter outside the run', () => {
    expect(refusal((d) => (d.chapters[0].tMs = 61_000)).path).toBe('$.chapters[0].tMs');
  });
});

describe('reading a parsed recording', () => {
  const rec = parseBoardRecording(fixtureJson());

  it('reports the whole run as the bounds, or an excerpt window', () => {
    expect(recordingBounds(rec)).toEqual({ fromMs: 0, toMs: 60_000 });
    const doc = fixtureJson();
    doc.excerpt = { fromMs: 0, toMs: 50_000 };
    expect(recordingBounds(parseBoardRecording(doc))).toEqual({ fromMs: 0, toMs: 50_000 });
  });

  it('converts an offset to wall time from the run start', () => {
    expect(recordedWallTimeMs(rec, 0)).toBe(FIXTURE_START_MS);
    expect(recordedWallTimeMs(rec, 1_234)).toBe(FIXTURE_START_MS + 1_234);
  });

  it('reports the simulation clock segment in force, and nothing before the first', () => {
    const doc = fixtureJson();
    doc.clock = [
      { fromMs: 11, mode: 'real', scale: 1 },
      { fromMs: 30_000, mode: 'scaled', scale: 60 },
    ];
    const r = parseBoardRecording(doc);
    expect(simulationScaleAt(r, 5)).toBeNull();
    expect(simulationScaleAt(r, 11)).toEqual({ mode: 'real', scale: 1 });
    expect(simulationScaleAt(r, 29_999)).toEqual({ mode: 'real', scale: 1 });
    expect(simulationScaleAt(r, 30_000)).toEqual({ mode: 'scaled', scale: 60 });
  });
});
