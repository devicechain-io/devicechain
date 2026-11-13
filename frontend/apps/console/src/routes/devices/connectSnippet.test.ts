// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The connectivity helper's commands, held to frontend/testdata/connect-snippets/cases.json.
//
// 🔴 THAT FILE IS READ BY TWO GO TESTS AS WELL, which run its commands through the real
// ingest code (see connectSnippet.ts). This test is the half that ties the console to the
// file: a builder change that is not written into the file fails here, and a change that
// is written into it is then judged by the platform rather than by this test. To rewrite
// the expected commands after a deliberate change, run this file with
// UPDATE_CONNECT_SNIPPETS=1 and let the Go tests decide whether the new commands work.

import { describe, expect, it } from 'vitest';
import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import {
  conforms,
  eventBody,
  exampleMeasurements,
  exampleValue,
  httpCommand,
  httpUplinkCommand,
  MAX_EXAMPLE_METRICS,
  mqttCommand,
  mqttUplinkCommand,
  PLACEHOLDER,
  shellQuote,
  usableCredential,
  type SnippetInput,
} from './connectSnippet';

const CASES_PATH = resolve(
  dirname(fileURLToPath(import.meta.url)),
  '../../../../../testdata/connect-snippets/cases.json',
);

interface GoldenCase {
  name: string;
  input: SnippetInput;
  mqtt: string;
  http: string;
  mqttUplink: string;
  httpUplink: string;
}

const cases = JSON.parse(readFileSync(CASES_PATH, 'utf8')) as GoldenCase[];

if (process.env.UPDATE_CONNECT_SNIPPETS === '1') {
  for (const c of cases) {
    c.mqtt = mqttCommand(c.input);
    c.http = httpCommand(c.input);
    c.mqttUplink = mqttUplinkCommand(c.input);
    c.httpUplink = httpUplinkCommand(c.input);
  }
  writeFileSync(CASES_PATH, JSON.stringify(cases, null, 2) + '\n');
}

describe('connect snippets match the file the platform tests read', () => {
  it('has cases', () => {
    // A file emptied by accident would otherwise pass every loop below, here and in Go.
    expect(cases.length).toBeGreaterThanOrEqual(6);
  });
  for (const c of cases) {
    it(c.name, () => {
      expect(mqttCommand(c.input)).toBe(c.mqtt);
      expect(httpCommand(c.input)).toBe(c.http);
      expect(mqttUplinkCommand(c.input)).toBe(c.mqttUplink);
      expect(httpUplinkCommand(c.input)).toBe(c.httpUplink);
    });
  }
});

describe('example values', () => {
  it('uses a value inside the bounds', () => {
    expect(exampleValue({ metricKey: 'p', dataType: 'DOUBLE', minValue: 100, maxValue: 200 })).toBe('100');
    expect(exampleValue({ metricKey: 'p', dataType: 'DOUBLE', maxValue: -3 })).toBe('-3');
    expect(exampleValue({ metricKey: 'n', dataType: 'INT', maxValue: -10 })).toBe('-10');
    expect(exampleValue({ metricKey: 'n', dataType: 'INT', minValue: 1.5, maxValue: 3 })).toBe('2');
  });

  it('offers every storable type its default when nothing constrains it', () => {
    // A default that failed its own check would silently drop the metric from the example.
    expect(exampleValue({ metricKey: 'd', dataType: 'DOUBLE' })).toBe('21.5');
    expect(exampleValue({ metricKey: 'i', dataType: 'INT' })).toBe('42');
    expect(exampleValue({ metricKey: 'b', dataType: 'BOOLEAN' })).toBe('true');
  });

  it('offers nothing when no value would be stored', () => {
    expect(exampleValue({ metricKey: 'n', dataType: 'INT', minValue: 5.5, maxValue: 5.9 })).toBeNull();
    expect(exampleValue({ metricKey: 's', dataType: 'STRING' })).toBeNull();
    expect(exampleValue({ metricKey: 'u', dataType: 'WHATEVER' })).toBeNull();
    expect(exampleValue({ metricKey: 'e', dataType: 'DOUBLE', allowedValues: ['high', 'low'] })).toBeNull();
  });

  it('takes the first allowed value that conforms', () => {
    expect(exampleValue({ metricKey: 'e', dataType: 'DOUBLE', allowedValues: ['x', '3.5'] })).toBe('3.5');
    expect(exampleValue({ metricKey: 'b', dataType: 'BOOLEAN', allowedValues: ['false'] })).toBe('false');
  });

  it('conforms rejects what ingest rejects', () => {
    expect(conforms({ metricKey: 'i', dataType: 'INT' }, '1.5')).toBe(false);
    expect(conforms({ metricKey: 'i', dataType: 'INT' }, '9223372036854775808')).toBe(false);
    expect(conforms({ metricKey: 'b', dataType: 'BOOLEAN' }, 'yes')).toBe(false);
    expect(conforms({ metricKey: 'd', dataType: 'DOUBLE', minValue: 1 }, '0.5')).toBe(false);
  });

  it('caps the example and falls back to an undeclared key', () => {
    const many = Array.from({ length: MAX_EXAMPLE_METRICS + 3 }, (_, i) => ({
      metricKey: `m${String(i).padStart(2, '0')}`,
      dataType: 'DOUBLE',
    }));
    const ex = exampleMeasurements(many);
    expect(Object.keys(ex.measurements)).toHaveLength(MAX_EXAMPLE_METRICS);
    expect(ex.omitted).toBe(3);

    const collide = exampleMeasurements([{ metricKey: 'temperature', dataType: 'STRING' }]);
    expect(collide).toEqual({ measurements: { reading: '21.5' }, omitted: 0, fallback: true });
  });
});

describe('secrets', () => {
  const base: SnippetInput = {
    tenant: 'acme',
    deviceToken: 'sensor-001',
    credential: { credentialType: 'MQTT_BASIC', credentialId: 'sensor-001-user' },
    metrics: [],
  };

  it('never carries an MQTT_BASIC password, only the placeholder', () => {
    const body = JSON.parse(eventBody(base)) as Record<string, unknown>;
    expect(body.credentialSecret).toBe(PLACEHOLDER.password);
    expect(mqttCommand(base)).toContain(`-P ${shellQuote(PLACEHOLDER.password)}`);
  });

  // The platform MQTT broker refuses an event whose body carries a credential, so the MQTT
  // snippet authenticates the connection (-u/-P) and its body names none of the three members.
  it('puts no credential in the MQTT body, for either credential type', () => {
    for (const credential of [base.credential, { credentialType: 'ACCESS_TOKEN' as const, credentialId: 'tok' }]) {
      const body = JSON.parse(eventBody({ ...base, credential }, 'mqtt')) as Record<string, unknown>;
      expect(body).not.toHaveProperty('credentialType');
      expect(body).not.toHaveProperty('credentialId');
      expect(body).not.toHaveProperty('credentialSecret');
      const command = mqttCommand({ ...base, credential });
      expect(command).not.toContain('credentialSecret');
      expect(command).not.toContain('credentialId');
    }
  });

  it('sends no password for an access token', () => {
    const tok: SnippetInput = { ...base, credential: { credentialType: 'ACCESS_TOKEN', credentialId: 'tok' } };
    expect(JSON.parse(eventBody(tok))).not.toHaveProperty('credentialSecret');
    expect(mqttCommand(tok)).not.toContain(' -P ');
  });
});

describe('raw-payload commands', () => {
  const base: SnippetInput = {
    tenant: 'acme',
    deviceToken: 'sensor-001',
    credential: { credentialType: 'MQTT_BASIC', credentialId: 'sensor-001-user' },
    metrics: [],
  };
  const tok: SnippetInput = { ...base, credential: { credentialType: 'ACCESS_TOKEN', credentialId: 'tok' } };

  it('sends a file untouched to its own uplink topic, carrying no event body', () => {
    const c = mqttUplinkCommand(base);
    expect(c).toContain(`-t '<instance-id>/acme/devices/sensor-001/uplink'`);
    expect(c).toContain('-f payload.bin');
    expect(c).not.toContain(' -m ');
    expect(c).not.toContain('credentialSecret');
  });

  it('presents the credential in headers, never a password', () => {
    expect(httpUplinkCommand(tok)).toContain(`-H 'Authorization: Bearer tok'`);
    const c = httpUplinkCommand(base);
    expect(c).toContain(`-H 'X-DeviceChain-Credential-Type: MQTT_BASIC'`);
    expect(c).toContain(`-H 'X-DeviceChain-Credential-Id: sensor-001-user'`);
    expect(c).toContain(`-H 'X-DeviceChain-Credential-Secret: ${PLACEHOLDER.password}'`);
    expect(c).not.toContain('Authorization');
    expect(c).toContain(`--data-binary '@payload.bin'`);
  });
});

describe('usableCredential', () => {
  const now = new Date('2026-10-16T00:00:00Z');
  const ok = { credentialType: 'ACCESS_TOKEN', credentialId: 'x', enabled: true, expiresAt: null };
  it('admits an enabled, unexpired credential of a supported type', () => {
    expect(usableCredential(ok, now)).toBe(true);
    expect(usableCredential({ ...ok, expiresAt: '2026-10-17T00:00:00Z' }, now)).toBe(true);
  });
  it('refuses a disabled, expired or unsupported one', () => {
    expect(usableCredential({ ...ok, enabled: false }, now)).toBe(false);
    expect(usableCredential({ ...ok, expiresAt: '2026-10-15T00:00:00Z' }, now)).toBe(false);
    expect(usableCredential({ ...ok, credentialType: 'X509_CERTIFICATE' }, now)).toBe(false);
  });
});
