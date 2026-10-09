// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import { parseBoardRecording, RecordingFormatError } from './recording';
import { FIXTURE_DEVICES, fixtureJson } from './testing/recording-fixture';

type Json = Record<string, any>;

// Parse a corrupted copy of the fixture and return the error it is refused with.
function refusal(mutate: (doc: Json) => void, options: { locations?: boolean } = {}): RecordingFormatError {
  const doc = JSON.parse(JSON.stringify(fixtureJson(options))) as Json;
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
  it('accepts the fixture, with and without positions', () => {
    const rec = parseBoardRecording(fixtureJson());
    expect(rec.devices).toEqual([...FIXTURE_DEVICES]);
    expect(rec.channels.locations).toBe(false);
    expect(rec.locations).toEqual([]);
    expect(parseBoardRecording(fixtureJson({ locations: true })).locations).toHaveLength(2);
  });

  it('accepts a document that has been through JSON text', () => {
    expect(() => parseBoardRecording(JSON.parse(JSON.stringify(fixtureJson())))).not.toThrow();
  });

  it('refuses anything that is not an object', () => {
    for (const bad of [null, [], 'x', 3, undefined]) {
      expect(() => parseBoardRecording(bad)).toThrow(RecordingFormatError);
    }
  });

  it('refuses an unknown key at the top level and at every nesting depth', () => {
    expect(refusal((d) => (d.extra = 1)).path).toBe('$.extra');
    expect(refusal((d) => (d.channels.extra = true)).path).toBe('$.channels.extra');
    expect(refusal((d) => (d.clock[0].extra = 1)).path).toBe('$.clock[0].extra');
    expect(refusal((d) => (d.anchors[0].extra = 1)).path).toBe('$.anchors[0].extra');
    expect(refusal((d) => (d.measurements[0].extra = [])).path).toBe('$.measurements[0].extra');
    expect(refusal((d) => (d.alarms[0].extra = 1)).path).toBe('$.alarms[0].extra');
    expect(refusal((d) => (d.chapters[0].extra = 1)).path).toBe('$.chapters[0].extra');
    expect(refusal((d) => (d.locations[0].extra = []), { locations: true }).path).toBe('$.locations[0].extra');
  });

  it('refuses a missing required key', () => {
    expect(refusal((d) => delete d.runId).path).toBe('$.runId');
    expect(refusal((d) => delete d.measurements[0].occ).path).toBe('$.measurements[0].occ');
  });

  it('refuses an unsupported or absent format version', () => {
    expect(refusal((d) => (d.formatVersion = 2)).path).toBe('$.formatVersion');
    expect(refusal((d) => (d.formatVersion = '1')).path).toBe('$.formatVersion');
  });

  it('refuses a ragged column', () => {
    expect(refusal((d) => d.measurements[0].v.pop()).path).toBe('$.measurements[0].v');
    expect(refusal((d) => d.measurements[0].occ.push(1)).path).toBe('$.measurements[0].occ');
    expect(refusal((d) => d.locations[0].lat.pop(), { locations: true }).path).toBe('$.locations[0].lat');
  });

  it('refuses a time axis that decreases or leaves the run', () => {
    expect(refusal((d) => (d.measurements[0].t[2] = 1)).path).toBe('$.measurements[0].t[2]');
    expect(refusal((d) => (d.measurements[0].t[4] = d.durationMs + 1)).path).toBe('$.measurements[0].t[4]');
    expect(refusal((d) => (d.measurements[0].t[0] = -1)).path).toBe('$.measurements[0].t[0]');
    expect(refusal((d) => (d.alarms[1].t = 1)).path).toBe('$.alarms[1].t');
  });

  it('refuses values of the wrong type, including non-finite numbers', () => {
    expect(refusal((d) => (d.measurements[0].v[0] = '90')).path).toBe('$.measurements[0].v[0]');
    expect(refusal((d) => (d.measurements[0].t[0] = null)).path).toBe('$.measurements[0].t[0]');
    expect(refusal((d) => (d.durationMs = 'long')).path).toBe('$.durationMs');
    expect(refusal((d) => (d.alarms[0].acknowledged = 'no')).path).toBe('$.alarms[0].acknowledged');
  });

  it('refuses a series naming an undeclared measurement or an unknown device', () => {
    expect(refusal((d) => (d.measurements[0].name = 'undeclared')).path).toBe('$.measurements[0].name');
    expect(refusal((d) => (d.measurements[0].device = 'nobody')).path).toBe('$.measurements[0].device');
    expect(refusal((d) => (d.alarms[0].device = 'nobody')).path).toBe('$.alarms[0].device');
    expect(refusal((d) => d.anchors[0].members.push('nobody')).path).toBe('$.anchors[0].members[3]');
  });

  it('refuses a duplicate device or a duplicate series', () => {
    expect(refusal((d) => d.devices.push(d.devices[0])).path).toBe('$.devices');
    expect(refusal((d) => d.measurements.push(structuredClone(d.measurements[0]))).path).toBe('$.measurements[4]');
  });

  it('refuses a channel claim the document cannot honour', () => {
    expect(refusal((d) => (d.channels.commands = true)).path).toBe('$.channels.commands');
    expect(refusal((d) => (d.channels.alarms = false)).path).toBe('$.alarms');
    expect(
      refusal((d) => {
        d.channels.locations = false;
      }, { locations: true }).path,
    ).toBe('$.locations');
  });

  it('refuses alarm rows whose state and timestamps disagree', () => {
    expect(refusal((d) => (d.alarms[0].cleared = 5)).path).toBe('$.alarms[0].cleared');
    expect(refusal((d) => (d.alarms[2].cleared = null)).path).toBe('$.alarms[2].cleared');
    expect(refusal((d) => (d.alarms[3].acked = null)).path).toBe('$.alarms[3].acked');
    expect(refusal((d) => (d.alarms[0].severity = 'SEVERE')).path).toBe('$.alarms[0].severity');
  });

  it('refuses coordinates outside the globe', () => {
    expect(refusal((d) => (d.locations[0].lat[0] = 91), { locations: true }).path).toBe('$.locations[0].lat[0]');
    expect(refusal((d) => (d.locations[0].lon[1] = -181), { locations: true }).path).toBe('$.locations[0].lon[1]');
  });

  it('refuses a malformed identity: hashes, build id, start instant', () => {
    expect(refusal((d) => (d.boardHash = 'xyz')).path).toBe('$.boardHash');
    expect(refusal((d) => (d.sourceHashes['observed.ndjson'] = 'A'.repeat(64))).path).toBe('$.sourceHashes.observed.ndjson');
    expect(refusal((d) => (d.buildSha = 'not-a-sha')).path).toBe('$.buildSha');
    expect(refusal((d) => (d.startedAtUtc = '2026-01-02 03:04:05')).path).toBe('$.startedAtUtc');
    expect(refusal((d) => (d.startedAtUtc = '2026-13-45T99:00:00Z')).path).toBe('$.startedAtUtc');
  });

  it('refuses a source-hash key that would rewrite the prototype', () => {
    const doc = fixtureJson();
    doc.sourceHashes = JSON.parse(`{"__proto__":"${'a'.repeat(64)}"}`);
    expect(() => parseBoardRecording(doc)).toThrow(/reserved key/);
  });

  it('refuses a clock that does not start at zero or does not increase', () => {
    expect(refusal((d) => (d.clock[0].fromMs = 5)).path).toBe('$.clock');
    expect(refusal((d) => (d.clock = [])).path).toBe('$.clock');
    expect(refusal((d) => d.clock.push({ fromMs: 0, scale: 2 })).path).toBe('$.clock[1].fromMs');
    expect(refusal((d) => (d.clock[0].scale = 0)).path).toBe('$.clock[0].scale');
  });
});
