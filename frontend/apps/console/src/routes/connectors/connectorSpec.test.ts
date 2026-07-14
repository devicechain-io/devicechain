// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, it, expect } from 'vitest';
import {
  specForType,
  emptyFormState,
  serializeConfig,
  deserializeConfig,
  validateConfigForm,
  type ConfigFormState,
} from './connectorSpec';

const spec = (t: string) => {
  const s = specForType(t);
  if (!s) throw new Error(`no spec for ${t}`);
  return s;
};

describe('serializeConfig', () => {
  it('mqtt: splits the URL list, coerces qos, omits empties', () => {
    const st = emptyFormState(spec('mqtt'));
    st.fields.urls = 'tcp://a:1883\ntcp://b:1883';
    st.fields.topic = 'alerts';
    st.fields.qos = '2';
    // clientId + username left empty → omitted
    const json = JSON.parse(serializeConfig(spec('mqtt'), st));
    expect(json).toEqual({ urls: ['tcp://a:1883', 'tcp://b:1883'], topic: 'alerts', qos: 2 });
  });

  it('mqtt: a blank qos is omitted so Bento defaults apply', () => {
    const st = emptyFormState(spec('mqtt'));
    st.fields.urls = 'tcp://a:1883';
    st.fields.topic = 't';
    const json = JSON.parse(serializeConfig(spec('mqtt'), st));
    expect(json.qos).toBeUndefined();
  });

  it('kafka: emits the SASL block only when enabled', () => {
    const st = emptyFormState(spec('kafka'));
    st.fields.addresses = 'b:9092';
    st.fields.topic = 't';
    st.fields.tls = true;
    expect(JSON.parse(serializeConfig(spec('kafka'), st)).sasl).toBeUndefined();

    st.saslEnabled = true;
    st.saslMechanism = 'SCRAM-SHA-256';
    st.saslUsername = 'svc';
    const json = JSON.parse(serializeConfig(spec('kafka'), st));
    expect(json).toEqual({
      addresses: ['b:9092'],
      topic: 't',
      tls: true,
      sasl: { mechanism: 'SCRAM-SHA-256', username: 'svc' },
    });
  });

  it('aws_sqs: emits region/url/accessKeyId (never the secret)', () => {
    const st = emptyFormState(spec('aws_sqs'));
    st.fields.region = 'us-east-1';
    st.fields.url = 'https://sqs/q';
    st.fields.accessKeyId = 'AKIA';
    const json = JSON.parse(serializeConfig(spec('aws_sqs'), st));
    expect(json).toEqual({ region: 'us-east-1', url: 'https://sqs/q', accessKeyId: 'AKIA' });
  });
});

describe('deserializeConfig round-trips', () => {
  const cases: Array<[string, ConfigFormState]> = [
    [
      'mqtt',
      {
        fields: { urls: 'tcp://a:1883\ntcp://b:1883', topic: 'alerts', qos: '1', clientId: 'dc', username: 'u' },
        saslEnabled: false,
        saslMechanism: 'PLAIN',
        saslUsername: '',
      },
    ],
    [
      'kafka',
      {
        fields: { addresses: 'b:9092', topic: 't', clientId: '', tls: true },
        saslEnabled: true,
        saslMechanism: 'PLAIN',
        saslUsername: 'svc',
      },
    ],
  ];
  it.each(cases)('%s form → JSON → form is stable', (type, form) => {
    const json = serializeConfig(spec(type), form);
    const back = deserializeConfig(spec(type), json);
    // Re-serializing the reconstructed form must yield identical JSON.
    expect(serializeConfig(spec(type), back)).toEqual(json);
  });

  it('tolerates a malformed config blob', () => {
    const back = deserializeConfig(spec('mqtt'), 'not json');
    expect(back.fields.topic).toBe('');
  });
});

describe('validateConfigForm', () => {
  it('flags a missing required field', () => {
    const st = emptyFormState(spec('mqtt'));
    st.fields.topic = 'alerts'; // urls still empty
    expect(validateConfigForm(spec('mqtt'), st)).toMatch(/Broker URLs/);
  });

  it('flags a SASL username missing when SASL is enabled', () => {
    const st = emptyFormState(spec('kafka'));
    st.fields.addresses = 'b:9092';
    st.fields.topic = 't';
    st.saslEnabled = true;
    expect(validateConfigForm(spec('kafka'), st)).toMatch(/SASL username/);
  });

  it('passes a complete config', () => {
    const st = emptyFormState(spec('aws_sns'));
    st.fields.region = 'us-east-1';
    st.fields.topicArn = 'arn:aws:sns:us-east-1:1:t';
    st.fields.accessKeyId = 'AKIA';
    expect(validateConfigForm(spec('aws_sns'), st)).toBeNull();
  });
});
