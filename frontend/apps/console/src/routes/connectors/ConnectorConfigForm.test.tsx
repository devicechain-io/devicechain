// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The connector config editor shared by the create drawer and the detail editor. It is a
// controlled form, so the harness holds the state the way a parent does and every assertion
// reads either the DOM or what the parent would SEND: the config JSON, the write-only secret
// argument, and the client-side validation message. The one rule under all of it: a stored
// credential never comes back into the form, and a secret goes out only when it was typed.
import '@/i18n/config';
import { useState } from 'react';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import i18n from '@/i18n/config';
import {
  ConnectorConfigForm,
  editorConfigJSON,
  editorFromConnector,
  editorSecretArg,
  newEditorState,
  secretRequiredForState,
  validateEditor,
  type ConnectorEditorState,
} from './ConnectorConfigForm';
import { selectOption } from '@/test/select';

afterEach(cleanup);

let latest: ConnectorEditorState;

function Harness({
  initial,
  mode = 'create',
  existingHasSecret = false,
}: {
  initial: ConnectorEditorState;
  mode?: 'create' | 'edit';
  existingHasSecret?: boolean;
}) {
  const [state, setState] = useState(initial);
  latest = state;
  return <ConnectorConfigForm state={state} onChange={setState} mode={mode} existingHasSecret={existingHasSecret} />;
}

const t = (key: string, options?: Record<string, unknown>) => i18n.t(key, { ns: 'connectors', ...options }) as string;
const validate = (existing = false) => validateEditor(latest, t, existing);
const type = (label: string | RegExp, value: string) =>
  fireEvent.change(screen.getByLabelText(label), { target: { value } });

describe('ConnectorConfigForm: a field per schema property', () => {
  it('renders the MQTT fields, with required ones starred and an optional password', () => {
    render(<Harness initial={newEditorState('mqtt')} />);
    expect(screen.getByLabelText('Broker URLs *')).toBeTruthy();
    expect(screen.getByLabelText('Topic *')).toBeTruthy();
    expect(screen.getByLabelText('Client ID')).toBeTruthy();
    expect(screen.getByLabelText('Username')).toBeTruthy();
    expect(screen.getByText('QoS')).toBeTruthy();
    expect(screen.getByLabelText('Password')).toBeTruthy();
    expect(screen.queryByLabelText('Password *')).toBeNull();
    expect(secretRequiredForState(latest)).toBe(false);
  });

  it('renders the AWS fields and marks the secret access key required', () => {
    render(<Harness initial={newEditorState('aws_sns')} />);
    expect(screen.getByLabelText('Region *')).toBeTruthy();
    expect(screen.getByLabelText('Topic ARN *')).toBeTruthy();
    expect(screen.getByLabelText('Access key ID *')).toBeTruthy();
    expect(screen.getByLabelText('Secret access key *')).toBeTruthy();
    expect(secretRequiredForState(latest)).toBe(true);
  });

  it('switches the whole field set when the type changes, and drops a typed secret', async () => {
    render(<Harness initial={newEditorState('mqtt')} />);
    type('Password', 'mqtt-password');
    expect(latest.secret.value).toBe('mqtt-password');
    await selectOption(screen.getByLabelText('Type'), 'AWS SQS');
    expect(await screen.findByLabelText('Queue URL *')).toBeTruthy();
    expect(screen.queryByLabelText('Topic *')).toBeNull();
    // A value typed for the old type must never be submitted as the new type's secret.
    expect(latest.secret).toEqual({ mode: 'set', value: '' });
    expect((screen.getByLabelText('Secret access key *') as HTMLInputElement).value).toBe('');
  });

  it('turns a typed form into the exact config JSON, omitting what is blank', () => {
    render(<Harness initial={newEditorState('mqtt')} />);
    type('Broker URLs *', 'tcp://a:1883, tcp://b:1883\ntcp://c:1883');
    type('Topic *', ' alerts/incidents ');
    type('Username', 'publisher');
    expect(JSON.parse(editorConfigJSON(latest))).toEqual({
      urls: ['tcp://a:1883', 'tcp://b:1883', 'tcp://c:1883'],
      topic: 'alerts/incidents',
      username: 'publisher',
    });
  });

  it('offers SASL for Kafka, asks for a username once it is on, and requires the password then', () => {
    render(<Harness initial={newEditorState('kafka')} />);
    expect(screen.queryByLabelText('SASL username *')).toBeNull();
    expect(secretRequiredForState(latest)).toBe(false);
    fireEvent.click(screen.getByLabelText('Use SASL authentication'));
    expect(screen.getByLabelText('SASL username *')).toBeTruthy();
    expect(screen.getByLabelText('SASL password *')).toBeTruthy();
    expect(secretRequiredForState(latest)).toBe(true);
    type('Broker addresses *', 'k1:9092');
    type('Topic *', 'device-events');
    type('SASL username *', 'svc');
    expect(JSON.parse(editorConfigJSON(latest))).toEqual({
      addresses: ['k1:9092'],
      topic: 'device-events',
      sasl: { mechanism: 'PLAIN', username: 'svc' },
    });
  });
});

describe('ConnectorConfigForm: validation blocks a bad config', () => {
  it('names the first required field that is empty', () => {
    render(<Harness initial={newEditorState('mqtt')} />);
    expect(validate()).toBe('Broker URLs is required.');
    type('Broker URLs *', 'tcp://a:1883');
    expect(validate()).toBe('Topic is required.');
    type('Topic *', 'alerts');
    expect(validate()).toBeNull();
  });

  it('requires the SASL username when SASL is on', () => {
    render(<Harness initial={newEditorState('kafka')} />);
    type('Broker addresses *', 'k1:9092');
    type('Topic *', 'events');
    fireEvent.click(screen.getByLabelText('Use SASL authentication'));
    expect(validate()).toBe('SASL username is required when SASL is enabled.');
    type('SASL username *', 'svc');
    expect(validate()).toBe('SASL password is required for Apache Kafka connectors.');
    type('SASL password *', 'hunter2');
    expect(validate()).toBeNull();
  });

  it('requires the AWS credential, and a typed one satisfies it', () => {
    render(<Harness initial={newEditorState('aws_sns')} />);
    type('Region *', 'us-east-1');
    type('Topic ARN *', 'arn:aws:sns:us-east-1:1:t');
    type('Access key ID *', 'AKIA');
    expect(validate()).toBe('Secret access key is required for AWS SNS connectors.');
    type('Secret access key *', 'shh');
    expect(validate()).toBeNull();
  });
});

describe('ConnectorConfigForm: the write-only credential', () => {
  const stored = editorFromConnector({
    type: 'aws_sns',
    config: JSON.stringify({ region: 'eu-west-1', topicArn: 'arn:t', accessKeyId: 'AKIA' }),
    hasSecret: true,
  });

  it('never pre-fills a stored secret: it shows "configured" and no input at all', () => {
    render(<Harness initial={stored} mode="edit" existingHasSecret />);
    expect(screen.getByText('Credential configured')).toBeTruthy();
    expect(document.querySelector('input[type="password"]')).toBeNull();
    // The config fields are prefilled from the stored document, the type is read-only.
    expect((screen.getByLabelText('Region *') as HTMLInputElement).value).toBe('eu-west-1');
    expect((screen.getByLabelText('Type') as HTMLInputElement).disabled).toBe(true);
  });

  it('sends no secret at all while it is kept, and a kept required secret satisfies validation', () => {
    render(<Harness initial={stored} mode="edit" existingHasSecret />);
    expect(editorSecretArg(latest, 'edit')).toBeUndefined();
    expect(validate(true)).toBeNull();
  });

  it('offers no Clear for a required credential, but Replace opens an empty input that sends what was typed', () => {
    render(<Harness initial={stored} mode="edit" existingHasSecret />);
    expect(screen.queryByRole('button', { name: 'Clear' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Replace' }));
    const input = screen.getByLabelText('Secret access key *') as HTMLInputElement;
    expect(input.type).toBe('password');
    expect(input.value).toBe('');
    expect(input.placeholder).toBe('Enter a new value');
    // Typed nothing yet: sending nothing, and validation refuses to save without a credential.
    expect(editorSecretArg(latest, 'edit')).toBeUndefined();
    expect(validate(false)).toBe('Secret access key is required for AWS SNS connectors.');
    fireEvent.change(input, { target: { value: 'new-secret' } });
    expect(editorSecretArg(latest, 'edit')).toBe('new-secret');
  });

  it('lets an optional credential be cleared (an empty string on edit) and the clear be undone', () => {
    const mqtt = editorFromConnector({
      type: 'mqtt',
      config: JSON.stringify({ urls: ['tcp://a:1883'], topic: 'alerts' }),
      hasSecret: true,
    });
    render(<Harness initial={mqtt} mode="edit" existingHasSecret />);
    fireEvent.click(screen.getByRole('button', { name: 'Clear' }));
    expect(screen.getByText('Credential will be cleared on save')).toBeTruthy();
    expect(editorSecretArg(latest, 'edit')).toBe('');
    fireEvent.click(screen.getByRole('button', { name: 'Undo' }));
    expect(screen.getByText('Credential configured')).toBeTruthy();
    expect(editorSecretArg(latest, 'edit')).toBeUndefined();
  });

  it('on create, an empty credential means none and a typed one is sent', () => {
    render(<Harness initial={newEditorState('mqtt')} />);
    expect(editorSecretArg(latest, 'create')).toBeUndefined();
    type('Password', 'p4ss');
    expect(editorSecretArg(latest, 'create')).toBe('p4ss');
  });
});

describe('ConnectorConfigForm: a type this build has no editor for', () => {
  const unknown = editorFromConnector({ type: 'gcp_pubsub', config: '{"project":"p"}', hasSecret: false });

  it('says so and edits the raw JSON instead of rendering fields', () => {
    render(<Harness initial={unknown} mode="edit" />);
    expect(screen.getByText(/No structured editor for type “gcp_pubsub”/)).toBeTruthy();
    expect((screen.getByLabelText('Config (JSON)') as HTMLTextAreaElement).value).toBe('{"project":"p"}');
    expect(screen.queryByLabelText('Topic *')).toBeNull();
    expect(editorConfigJSON(latest)).toBe('{"project":"p"}');
  });

  it('refuses malformed JSON and accepts it once it parses', () => {
    render(<Harness initial={unknown} mode="edit" />);
    type('Config (JSON)', '{"project":');
    expect(validate()).toBe('Config must be valid JSON.');
    type('Config (JSON)', '{"project":"q"}');
    expect(validate()).toBeNull();
    expect(editorConfigJSON(latest)).toBe('{"project":"q"}');
  });
});
