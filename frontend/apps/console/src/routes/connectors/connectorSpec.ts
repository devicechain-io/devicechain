// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Per-type connector config field metadata + (de)serialization between a flat
// form state and the connector `config` JSON string.
//
// This MIRRORS the backend connectorspec generators (outbound-connectors/
// connectorspec/{mqtt,kafka,aws}.go) — the JSON field names here MUST match those
// struct tags exactly. It is an authoring aid only: the backend re-validates every
// config at create/update and again at publish (connectorspec.ValidateConfig), so
// the server remains the authoritative gate and a drift surfaces as a save error
// rather than a bad stored config. The credential (mqtt password / kafka SASL
// password / AWS secret access key) is NEVER part of `config` — it travels as the
// write-only `secret` and is sealed in the ADR-059 store.

export type FieldKind = 'text' | 'list' | 'qos' | 'bool';

export interface ConfigField {
  // The JSON key in the connector config object (matches the Go struct tag).
  key: string;
  label: string;
  kind: FieldKind;
  required?: boolean;
  placeholder?: string;
  description?: string;
}

export interface SecretSpec {
  label: string;
  description: string;
  // Whether a credential is always required for this type (AWS). kafka is
  // conditionally required (only when SASL is enabled) — handled in the form.
  required: boolean;
}

export interface ConnectorTypeSpec {
  type: string;
  // Human-facing name for the type dropdown.
  label: string;
  fields: ConfigField[];
  secret: SecretSpec;
  // kafka-only: an optional SASL block ({mechanism, username}) whose password is
  // the connector secret. Rendered as an enable-toggle + mechanism + username.
  sasl?: boolean;
}

// SASL mechanisms the backend accepts (kafka.go validateKafka).
export const SASL_MECHANISMS = ['PLAIN', 'SCRAM-SHA-256', 'SCRAM-SHA-512'] as const;

// The v1 registered types (connectorspec.builders). gcp_pubsub is deferred (no
// per-connector credential field), so it is intentionally absent — a connector of
// an unknown/unsupported type still renders via a raw-JSON fallback in the form.
export const CONNECTOR_TYPE_SPECS: ConnectorTypeSpec[] = [
  {
    type: 'mqtt',
    label: 'MQTT',
    fields: [
      {
        key: 'urls',
        label: 'Broker URLs',
        kind: 'list',
        required: true,
        placeholder: 'tcp://broker:1883',
        description: 'One per line. At least one is required.',
      },
      { key: 'topic', label: 'Topic', kind: 'text', required: true, placeholder: 'alerts/incidents' },
      { key: 'qos', label: 'QoS', kind: 'qos', description: 'Delivery quality of service. Default 1.' },
      {
        key: 'clientId',
        label: 'Client ID',
        kind: 'text',
        placeholder: 'devicechain',
        description: 'Optional. A per-connection suffix is appended so many senders can share it.',
      },
      { key: 'username', label: 'Username', kind: 'text', placeholder: 'publisher' },
    ],
    secret: {
      label: 'Password',
      description: 'The broker password for the username above. Optional.',
      required: false,
    },
  },
  {
    type: 'kafka',
    label: 'Apache Kafka',
    fields: [
      {
        key: 'addresses',
        label: 'Broker addresses',
        kind: 'list',
        required: true,
        placeholder: 'broker:9092',
        description: 'host:port, one per line. At least one is required.',
      },
      { key: 'topic', label: 'Topic', kind: 'text', required: true, placeholder: 'device-events' },
      { key: 'clientId', label: 'Client ID', kind: 'text', placeholder: 'devicechain' },
      { key: 'tls', label: 'Use TLS', kind: 'bool', description: 'Connect to the brokers over TLS.' },
    ],
    sasl: true,
    secret: {
      label: 'SASL password',
      description: 'Required when SASL is enabled; the password for the SASL user.',
      required: false,
    },
  },
  {
    type: 'aws_sns',
    label: 'AWS SNS',
    fields: [
      { key: 'region', label: 'Region', kind: 'text', required: true, placeholder: 'us-east-1' },
      {
        key: 'topicArn',
        label: 'Topic ARN',
        kind: 'text',
        required: true,
        placeholder: 'arn:aws:sns:us-east-1:123456789012:alerts',
      },
      {
        key: 'accessKeyId',
        label: 'Access key ID',
        kind: 'text',
        required: true,
        placeholder: 'AKIA…',
        description: 'Static credentials are required; ambient/instance-role credentials are not used.',
      },
      { key: 'endpoint', label: 'Endpoint override', kind: 'text', placeholder: 'https://…', description: 'Optional custom endpoint (e.g. a VPC endpoint or a test double).' },
    ],
    secret: {
      label: 'Secret access key',
      description: 'The AWS secret access key paired with the access key ID above. Required.',
      required: true,
    },
  },
  {
    type: 'aws_sqs',
    label: 'AWS SQS',
    fields: [
      { key: 'region', label: 'Region', kind: 'text', required: true, placeholder: 'us-east-1' },
      {
        key: 'url',
        label: 'Queue URL',
        kind: 'text',
        required: true,
        placeholder: 'https://sqs.us-east-1.amazonaws.com/123456789012/alerts',
      },
      {
        key: 'accessKeyId',
        label: 'Access key ID',
        kind: 'text',
        required: true,
        placeholder: 'AKIA…',
        description: 'Static credentials are required; ambient/instance-role credentials are not used.',
      },
      { key: 'endpoint', label: 'Endpoint override', kind: 'text', placeholder: 'https://…', description: 'Optional custom endpoint.' },
    ],
    secret: {
      label: 'Secret access key',
      description: 'The AWS secret access key paired with the access key ID above. Required.',
      required: true,
    },
  },
];

export function specForType(type: string): ConnectorTypeSpec | undefined {
  return CONNECTOR_TYPE_SPECS.find((s) => s.type === type);
}

// A flat, editable form state. List fields are held as the raw multiline string
// the user types; conversion to string[] happens at serialize time. `sasl*` fields
// back the kafka SASL block. Values are strings/booleans only.
export interface ConfigFormState {
  fields: Record<string, string | boolean>;
  saslEnabled: boolean;
  saslMechanism: string;
  saslUsername: string;
}

export function emptyFormState(spec: ConnectorTypeSpec | undefined): ConfigFormState {
  const fields: Record<string, string | boolean> = {};
  spec?.fields.forEach((f) => {
    fields[f.key] = f.kind === 'bool' ? false : '';
  });
  return { fields, saslEnabled: false, saslMechanism: 'PLAIN', saslUsername: '' };
}

// splitList turns a multiline/comma string into a trimmed, non-empty string[].
function splitList(raw: string): string[] {
  return raw
    .split(/[\n,]/)
    .map((s) => s.trim())
    .filter((s) => s.length > 0);
}

// serializeConfig assembles the connector `config` JSON from the form state,
// following each field's kind. Empty optional fields are omitted so Bento's own
// defaults apply (the Go structs use omitempty). Returns a compact JSON string.
export function serializeConfig(spec: ConnectorTypeSpec, state: ConfigFormState): string {
  const out: Record<string, unknown> = {};
  for (const f of spec.fields) {
    const v = state.fields[f.key];
    switch (f.kind) {
      case 'list': {
        const list = splitList(typeof v === 'string' ? v : '');
        if (list.length > 0) out[f.key] = list;
        break;
      }
      case 'qos': {
        const s = typeof v === 'string' ? v.trim() : '';
        if (s !== '') out[f.key] = Number(s);
        break;
      }
      case 'bool': {
        if (v === true) out[f.key] = true;
        break;
      }
      default: {
        const s = typeof v === 'string' ? v.trim() : '';
        if (s !== '') out[f.key] = s;
      }
    }
  }
  if (spec.sasl && state.saslEnabled) {
    out.sasl = { mechanism: state.saslMechanism, username: state.saslUsername.trim() };
  }
  return JSON.stringify(out);
}

// deserializeConfig reconstructs the form state from a stored `config` JSON string
// (an edit / rollback). Unknown keys are ignored; a malformed blob yields an empty
// state (the form still renders and the author can rebuild it).
export function deserializeConfig(spec: ConnectorTypeSpec, config: string): ConfigFormState {
  const state = emptyFormState(spec);
  let obj: Record<string, unknown>;
  try {
    obj = JSON.parse(config) as Record<string, unknown>;
  } catch {
    return state;
  }
  for (const f of spec.fields) {
    const v = obj[f.key];
    if (v === undefined || v === null) continue;
    switch (f.kind) {
      case 'list':
        state.fields[f.key] = Array.isArray(v) ? v.map(String).join('\n') : String(v);
        break;
      case 'qos':
        state.fields[f.key] = String(v);
        break;
      case 'bool':
        state.fields[f.key] = v === true;
        break;
      default:
        state.fields[f.key] = String(v);
    }
  }
  const sasl = obj.sasl;
  if (spec.sasl && sasl && typeof sasl === 'object') {
    const s = sasl as Record<string, unknown>;
    state.saslEnabled = true;
    if (typeof s.mechanism === 'string') state.saslMechanism = s.mechanism;
    if (typeof s.username === 'string') state.saslUsername = s.username;
  }
  return state;
}

// Client-side required-field check, mirroring the backend validators so the author
// gets a fast, specific error before the round-trip. Not authoritative — the server
// re-validates. Returns a human message, or null when the shape looks complete.
export function validateConfigForm(
  spec: ConnectorTypeSpec,
  state: ConfigFormState,
): string | null {
  for (const f of spec.fields) {
    if (!f.required) continue;
    const v = state.fields[f.key];
    if (f.kind === 'list') {
      if (splitList(typeof v === 'string' ? v : '').length === 0) {
        return `${f.label} is required.`;
      }
    } else if (typeof v === 'string' && v.trim() === '') {
      return `${f.label} is required.`;
    }
  }
  if (spec.sasl && state.saslEnabled && state.saslUsername.trim() === '') {
    return 'SASL username is required when SASL is enabled.';
  }
  return null;
}
