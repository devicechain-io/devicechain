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
  // i18n key in the `connectors` namespace — the caller resolves it with t(),
  // NOT a literal string (ADR-066). The `jsx-only` lint can't see text in this
  // data module, so keeping these as keys is what keeps the config form localized.
  label: string;
  kind: FieldKind;
  required?: boolean;
  // A literal example value shown in the field — an identifier/URL sample, not
  // translated.
  placeholder?: string;
  // i18n key (see `label`), or omitted.
  description?: string;
}

export interface SecretSpec {
  // i18n key in the `connectors` namespace (resolved by the caller), not a literal.
  label: string;
  // i18n key in the `connectors` namespace (resolved by the caller), not a literal.
  description: string;
  // Whether a credential is always required for this type (AWS). kafka is
  // conditionally required (only when SASL is enabled) — handled in the form.
  required: boolean;
}

export interface ConnectorTypeSpec {
  type: string;
  // Human-facing name for the type dropdown — a product/proper noun (MQTT, Apache
  // Kafka, AWS SNS), NOT localized, so this stays a literal (unlike the field and
  // secret labels above, which are i18n keys).
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
        label: 'fldBrokerUrls',
        kind: 'list',
        required: true,
        placeholder: 'tcp://broker:1883',
        description: 'descBrokerUrls',
      },
      { key: 'topic', label: 'fldTopic', kind: 'text', required: true, placeholder: 'alerts/incidents' },
      { key: 'qos', label: 'fldQos', kind: 'qos', description: 'descQos' },
      {
        key: 'clientId',
        label: 'fldClientId',
        kind: 'text',
        placeholder: 'devicechain',
        description: 'descMqttClientId',
      },
      { key: 'username', label: 'fldUsername', kind: 'text', placeholder: 'publisher' },
    ],
    secret: {
      label: 'secretPassword',
      description: 'secretPasswordDesc',
      required: false,
    },
  },
  {
    type: 'kafka',
    label: 'Apache Kafka',
    fields: [
      {
        key: 'addresses',
        label: 'fldBrokerAddresses',
        kind: 'list',
        required: true,
        placeholder: 'broker:9092',
        description: 'descKafkaAddresses',
      },
      { key: 'topic', label: 'fldTopic', kind: 'text', required: true, placeholder: 'device-events' },
      { key: 'clientId', label: 'fldClientId', kind: 'text', placeholder: 'devicechain' },
      { key: 'tls', label: 'fldUseTls', kind: 'bool', description: 'descKafkaTls' },
    ],
    sasl: true,
    secret: {
      label: 'secretSaslPassword',
      description: 'secretSaslPasswordDesc',
      required: false,
    },
  },
  {
    type: 'aws_sns',
    label: 'AWS SNS',
    fields: [
      { key: 'region', label: 'fldRegion', kind: 'text', required: true, placeholder: 'us-east-1' },
      {
        key: 'topicArn',
        label: 'fldTopicArn',
        kind: 'text',
        required: true,
        placeholder: 'arn:aws:sns:us-east-1:123456789012:alerts',
      },
      {
        key: 'accessKeyId',
        label: 'fldAccessKeyId',
        kind: 'text',
        required: true,
        placeholder: 'AKIA…',
        description: 'descAwsStaticCreds',
      },
      { key: 'endpoint', label: 'fldEndpointOverride', kind: 'text', placeholder: 'https://…', description: 'descSnsEndpoint' },
    ],
    secret: {
      label: 'secretAccessKey',
      description: 'secretAccessKeyDesc',
      required: true,
    },
  },
  {
    type: 'aws_sqs',
    label: 'AWS SQS',
    fields: [
      { key: 'region', label: 'fldRegion', kind: 'text', required: true, placeholder: 'us-east-1' },
      {
        key: 'url',
        label: 'fldQueueUrl',
        kind: 'text',
        required: true,
        placeholder: 'https://sqs.us-east-1.amazonaws.com/123456789012/alerts',
      },
      {
        key: 'accessKeyId',
        label: 'fldAccessKeyId',
        kind: 'text',
        required: true,
        placeholder: 'AKIA…',
        description: 'descAwsStaticCreds',
      },
      { key: 'endpoint', label: 'fldEndpointOverride', kind: 'text', placeholder: 'https://…', description: 'descSqsEndpoint' },
    ],
    secret: {
      label: 'secretAccessKey',
      description: 'secretAccessKeyDesc',
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
// `t` is the caller's `connectors`-namespace translator (this is a plain utility,
// not a component); `f.label` is an i18n key, so the required message interpolates
// the RESOLVED label into the `fieldRequired` sentence.
export function validateConfigForm(
  spec: ConnectorTypeSpec,
  state: ConfigFormState,
  t: (key: string, options?: Record<string, unknown>) => string,
): string | null {
  for (const f of spec.fields) {
    if (!f.required) continue;
    const v = state.fields[f.key];
    if (f.kind === 'list') {
      if (splitList(typeof v === 'string' ? v : '').length === 0) {
        return t('fieldRequired', { label: t(f.label) });
      }
    } else if (typeof v === 'string' && v.trim() === '') {
      return t('fieldRequired', { label: t(f.label) });
    }
  }
  if (spec.sasl && state.saslEnabled && state.saslUsername.trim() === '') {
    return t('saslUsernameRequired');
  }
  return null;
}
