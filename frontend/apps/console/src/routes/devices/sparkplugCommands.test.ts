// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The two rules the Sparkplug command surface rests on. The write payload is checked
// against what the adapter will accept (the JSON class must match the datatype the node
// declared), and the outcome reading is checked against the evidence the adapter stores,
// including the shapes it must NOT read as evidence.
import { describe, expect, it } from 'vitest';
import en from '../../i18n/locales/en/devices.json';
import es from '../../i18n/locales/es/devices.json';
import { SPARKPLUG_REFUSAL_REASONS, buildSparkplugWritePayload, sparkplugOutcome } from './sparkplugCommands';

const build = (metric: string, value: string, mode: 'auto' | 'text' = 'auto') =>
  buildSparkplugWritePayload(metric, value, mode);

describe('buildSparkplugWritePayload', () => {
  it('sends true and false as booleans, numbers as the literal typed, anything else as a string', () => {
    expect(build('Outputs/V1', 'true').payload).toBe('{"metric":"Outputs/V1","value":true}');
    expect(build('Outputs/V1', ' false ').payload).toBe('{"metric":"Outputs/V1","value":false}');
    expect(build('m', '42').payload).toBe('{"metric":"m","value":42}');
    expect(build('m', '-7').payload).toBe('{"metric":"m","value":-7}');
    // Written exactly as typed: 1.0 is not 1, and a 64-bit integer keeps every digit.
    expect(build('m', '1.0').payload).toBe('{"metric":"m","value":1.0}');
    expect(build('m', '1e3').payload).toBe('{"metric":"m","value":1e3}');
    expect(build('m', '9223372036854775807').payload).toBe('{"metric":"m","value":9223372036854775807}');
    expect(build('m', 'open').payload).toBe('{"metric":"m","value":"open"}');
  });

  it('does not read things JSON would not read as numbers', () => {
    for (const v of ['0x10', '1_000', 'Infinity', 'NaN', '+5', '.5', '5.', '01', '1,5', 'True']) {
      expect(build('m', v).payload, v).toBe(`{"metric":"m","value":${JSON.stringify(v)}}`);
    }
  });

  it('text mode always sends a string, so a String metric can hold "123" or "true"', () => {
    expect(build('m', '123', 'text').payload).toBe('{"metric":"m","value":"123"}');
    expect(build('m', 'true', 'text').payload).toBe('{"metric":"m","value":"true"}');
    // Escaped, so the text cannot break out of the payload.
    expect(build('m', 'a"b\\c', 'text').payload).toBe('{"metric":"m","value":"a\\"b\\\\c"}');
    // An empty string is a legitimate value for a String metric once the operator chose text.
    expect(build('m', '', 'text').payload).toBe('{"metric":"m","value":""}');
  });

  it('refuses a missing metric or (in detect mode) a missing value, sending nothing', () => {
    expect(build('  ', '1')).toEqual({ errors: { metric: 'metricRequired' } });
    expect(build('m', '   ')).toEqual({ errors: { value: 'valueRequired' } });
    expect(build('', '')).toEqual({ errors: { metric: 'metricRequired', value: 'valueRequired' } });
  });

  it('trims the metric name and escapes it', () => {
    expect(build('  Outputs/"V"  ', '1').payload).toBe('{"metric":"Outputs/\\"V\\"","value":1}');
  });
});

const evidence = (o: Record<string, unknown>) => JSON.stringify(o);

describe('sparkplugOutcome', () => {
  it('reads a confirmed write off the adapter evidence', () => {
    const v = sparkplugOutcome({
      name: 'sparkplug.write',
      status: 'SUCCESSFUL',
      responsePayload: evidence({
        kind: 'write',
        reason: 'confirmed',
        metric: 'Outputs/V1',
        value: true,
        reportedAt: '2026-10-27T12:00:00Z',
        via: 'DDATA',
        hostRole: 'primary',
      }),
    });
    expect(v.outcome).toEqual({
      kind: 'confirmedByReport',
      metric: 'Outputs/V1',
      value: 'true',
      at: '2026-10-27T12:00:00Z',
      via: 'DDATA',
    });
    expect(v.secondaryHost).toBe(false);
  });

  it('shows a string value without its quotes', () => {
    const v = sparkplugOutcome({
      name: 'sparkplug.write',
      status: 'SUCCESSFUL',
      responsePayload: evidence({ kind: 'write', reason: 'confirmed', metric: 'Mode', value: 'auto', via: 'NDATA', hostRole: 'primary' }),
    });
    expect(v.outcome).toMatchObject({ kind: 'confirmedByReport', value: 'auto' });
  });

  it('says "already at the value" for UNCHANGED, and not "confirmed"', () => {
    const v = sparkplugOutcome({
      name: 'sparkplug.write',
      status: 'SUCCESSFUL',
      responsePayload: evidence({
        kind: 'write',
        reason: 'unchanged',
        metric: 'm',
        value: 1,
        via: 'UNCHANGED',
        since: '2026-10-27T11:00:00Z',
        hostRole: 'primary',
      }),
    });
    expect(v.outcome).toEqual({ kind: 'alreadyAtValue', metric: 'm', value: '1', since: '2026-10-27T11:00:00Z' });
  });

  it('reads a rebirth or reboot as confirmed by the birth', () => {
    for (const kind of ['rebirth', 'reboot']) {
      const v = sparkplugOutcome({
        name: `sparkplug.${kind}`,
        status: 'SUCCESSFUL',
        responsePayload: evidence({ kind, reason: 'confirmed', reportedAt: '2026-10-27T12:00:00Z', via: 'NBIRTH', hostRole: 'primary' }),
      });
      expect(v.outcome, kind).toEqual({ kind: 'confirmedByBirth', at: '2026-10-27T12:00:00Z' });
    }
  });

  const reasonOf = (reason: string) => JSON.stringify({ reason });

  it('says a TIMEOUT that ran out its window is unconfirmed, and SENT is still waiting', () => {
    expect(
      sparkplugOutcome({ name: 'sparkplug.write', status: 'TIMEOUT', responsePayload: reasonOf('unconfirmed_timeout') }).outcome,
    ).toEqual({ kind: 'unconfirmed' });
    expect(sparkplugOutcome({ name: 'sparkplug.write', status: 'SENT' }).outcome).toEqual({ kind: 'inFlight' });
  });

  it('tells a superseded or session-dropped command apart by its reason code', () => {
    expect(
      sparkplugOutcome({ name: 'sparkplug.write', status: 'TIMEOUT', responsePayload: reasonOf('superseded') }).outcome,
    ).toEqual({ kind: 'superseded' });
    expect(
      sparkplugOutcome({ name: 'sparkplug.write', status: 'TIMEOUT', responsePayload: reasonOf('session_dropped') }).outcome,
    ).toEqual({ kind: 'likelyRefused' });
  });

  it('tells a command refused before the wire apart by its reason code, and ignores one it does not know', () => {
    for (const reason of SPARKPLUG_REFUSAL_REASONS) {
      expect(
        sparkplugOutcome({ name: 'sparkplug.write', status: 'FAILED', responsePayload: reasonOf(reason) }).outcome,
        reason,
      ).toEqual({ kind: 'refused', reason });
    }
    expect(
      sparkplugOutcome({ name: 'sparkplug.write', status: 'FAILED', responsePayload: reasonOf('something_new') }).outcome,
    ).toBeNull();
    // A refusal code on a command that was not refused says nothing.
    expect(
      sparkplugOutcome({ name: 'sparkplug.write', status: 'SUCCESSFUL', responsePayload: reasonOf('metric_not_writable') }).outcome,
    ).toBeNull();
  });

  it('has a sentence in every locale for every refusal reason', () => {
    for (const catalog of [en, es] as Record<string, string>[]) {
      for (const reason of SPARKPLUG_REFUSAL_REASONS) {
        expect(catalog[`sparkplugRefused_${reason}`], reason).toBeTruthy();
      }
    }
  });

  it('says nothing extra for a reason it does not know, or none, however the error text reads', () => {
    for (const responsePayload of [reasonOf('source_disabled'), reasonOf('something_new'), reasonOf(''), '{}', null, undefined]) {
      expect(sparkplugOutcome({ name: 'sparkplug.write', status: 'TIMEOUT', responsePayload }).outcome, String(responsePayload)).toBeNull();
    }
    // A success with an unknown code is not "confirmed" either.
    expect(
      sparkplugOutcome({
        name: 'sparkplug.write',
        status: 'SUCCESSFUL',
        responsePayload: evidence({ kind: 'write', reason: 'brand_new', metric: 'm', value: 1, via: 'DDATA' }),
      }).outcome,
    ).toBeNull();
  });

  it('flags a confirmation that came from a secondary host', () => {
    const v = sparkplugOutcome({
      name: 'sparkplug.write',
      status: 'SUCCESSFUL',
      responsePayload: evidence({ kind: 'write', reason: 'confirmed', metric: 'm', value: 1, via: 'DDATA', hostRole: 'secondary' }),
    });
    expect(v.secondaryHost).toBe(true);
    expect(v.outcome).toMatchObject({ kind: 'confirmedByReport' });
  });

  it('says nothing for a failure (its own error is shown), another transport, or evidence it cannot read', () => {
    expect(sparkplugOutcome({ name: 'sparkplug.write', status: 'FAILED' })).toEqual({
      outcome: null,
      secondaryHost: false,
    });
    expect(sparkplugOutcome({ name: 'lwm2m.reboot', status: 'TIMEOUT' })).toEqual({
      outcome: null,
      secondaryHost: false,
    });
    expect(
      sparkplugOutcome({ name: 'reboot', status: 'SUCCESSFUL', responsePayload: evidence({ reason: 'confirmed', via: 'DDATA' }) }).outcome,
    ).toBeNull();
    for (const responsePayload of [null, '', 'not json', '[]', '"x"', '{}', evidence({ via: 3 })]) {
      expect(
        sparkplugOutcome({ name: 'sparkplug.write', status: 'SUCCESSFUL', responsePayload }).outcome,
        String(responsePayload),
      ).toBeNull();
    }
  });
});
