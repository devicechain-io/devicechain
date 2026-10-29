// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The two pure halves of the Sparkplug command surface: building the payload of a
// `sparkplug.write` from what an operator typed, and reading what happened to a
// `sparkplug.*` command off its stored result. The components that show them
// (SparkplugWriteForm, SparkplugOutcomeNote) stay thin so both rules can be pinned here.
//
// The keys must equal the adapter's command keys (sparkplug-ingest: sparkplug.write,
// sparkplug.rebirth, sparkplug.reboot).

export const SPARKPLUG_WRITE_KEY = 'sparkplug.write';
export const SPARKPLUG_REBIRTH_KEY = 'sparkplug.rebirth';
export const SPARKPLUG_REBOOT_KEY = 'sparkplug.reboot';

export const isSparkplugCommand = (name: string): boolean => name.startsWith('sparkplug.');

// ---------------------------------------------------------------------------------------
// The write payload.
//
// The adapter encodes `value` against the datatype the node declared in its birth, and it is
// strict about the JSON class: a Boolean metric takes `true`/`false` and nothing else, an
// integer takes a number, a String takes a string. The console does not know the datatype
// (the birth is the adapter's), so the operator's free text has to become the right JSON
// scalar:
//
//   auto  `true` / `false` -> a boolean; anything JSON reads as a number -> that number,
//         written exactly as typed (so 1.0 stays 1.0 and a 64-bit integer keeps every
//         digit); everything else -> a string.
//   text  always a string. This is the way to write "123" or "true" to a String metric, which
//         auto would send as a number or a boolean and the adapter would refuse.
//
// An empty value is refused in auto (nothing was typed) and sent as the empty string in text
// (the operator chose to send text, and an empty one is a valid value for a String metric).
// ---------------------------------------------------------------------------------------

export type SparkplugValueMode = 'auto' | 'text';

export type SparkplugWriteErrors = { metric?: 'metricRequired'; value?: 'valueRequired' };

// The JSON number grammar. Not Number(): that accepts '', ' 0x10 ', 'Infinity' and '1_000'.
const JSON_NUMBER = /^-?(0|[1-9]\d*)(\.\d+)?([eE][+-]?\d+)?$/;

export function buildSparkplugWritePayload(
  metric: string,
  value: string,
  mode: SparkplugValueMode,
): { payload?: string; errors: SparkplugWriteErrors } {
  const errors: SparkplugWriteErrors = {};
  const name = metric.trim();
  if (name === '') errors.metric = 'metricRequired';

  let literal: string;
  if (mode === 'text') {
    literal = JSON.stringify(value);
  } else {
    const v = value.trim();
    if (v === '') {
      errors.value = 'valueRequired';
      literal = '""';
    } else if (v === 'true' || v === 'false') {
      literal = v;
    } else if (JSON_NUMBER.test(v)) {
      literal = v;
    } else {
      literal = JSON.stringify(value.trim());
    }
  }
  if (errors.metric || errors.value) return { errors };
  return { payload: `{"metric":${JSON.stringify(name)},"value":${literal}}`, errors };
}

// ---------------------------------------------------------------------------------------
// What happened to a command.
//
// Sparkplug has no command acknowledgement. The adapter publishes the command and then
// waits for the node to REPORT the requested value, so what the platform knows is a report,
// never an obeyed command:
//
//   SUCCESSFUL, via a data or birth message   the node reported the value (evidence below)
//   SUCCESSFUL, via UNCHANGED                 the node already held the value and nothing
//                                             contrary arrived; the platform did not see it
//                                             act on this command
//   TIMEOUT, window ran out                   no report in the window: ignored, rejected, or
//                                             never published
//   TIMEOUT, superseded / broker dropped      told apart by the stored reason code
//   FAILED, with a refusal reason             refused before anything was sent, and why
//   SENT                                      published; the window is still open
//
// The evidence is the JSON the adapter stores as the command's response. It is parsed
// defensively: a payload that is not the adapter's (a hand-declared command that shares the
// prefix, an older adapter) yields no evidence rather than a wrong sentence.
// ---------------------------------------------------------------------------------------

// The reasons the adapter gives for refusing a command before it reached the wire (status FAILED,
// NOT_DELIVERED). A closed set, pinned on the adapter side by reasons_test.go: a code that is not
// here says nothing extra rather than a guess.
export const SPARKPLUG_REFUSAL_REASONS = [
  'commands_not_enabled',
  'broker_refused',
  'expired',
  'invalid_parameters',
  'unknown_command',
  'metric_protocol_control',
  'metric_not_writable',
  'metric_not_declared',
  'datatype_unsupported',
  'value_not_accepted',
  'datatype_changed',
  'node_rebirthing',
  'node_offline',
  'reboot_impact',
  'reboot_not_allowed',
  'reboot_not_a_node',
  'reboot_not_declared',
  'reboot_not_boolean',
  'reboot_unverified',
  'reboot_rate_limited',
  'no_dispatch',
  'publish_failed',
] as const;

export type SparkplugRefusalReason = (typeof SPARKPLUG_REFUSAL_REASONS)[number];

const isRefusalReason = (r: string): r is SparkplugRefusalReason =>
  (SPARKPLUG_REFUSAL_REASONS as readonly string[]).includes(r);

export type SparkplugOutcome =
  | { kind: 'inFlight' }
  | { kind: 'refused'; reason: SparkplugRefusalReason }
  | { kind: 'unconfirmed' }
  | { kind: 'superseded' }
  | { kind: 'likelyRefused' }
  | { kind: 'confirmedByReport'; metric?: string; value?: string; at?: string; via: string }
  | { kind: 'confirmedByBirth'; at?: string }
  | { kind: 'alreadyAtValue'; metric?: string; value?: string; since?: string };

export type SparkplugOutcomeView = {
  outcome: SparkplugOutcome | null;
  // The command was sent by a host that is not the nodes' primary: Sparkplug recommends that
  // only the primary host command, so a node or its broker may ignore the command and the
  // primary may overwrite the value.
  secondaryHost: boolean;
};

type Evidence = {
  reason?: unknown;
  kind?: unknown;
  metric?: unknown;
  value?: unknown;
  reportedAt?: unknown;
  via?: unknown;
  since?: unknown;
  hostRole?: unknown;
};

const str = (v: unknown): string | undefined => (typeof v === 'string' && v !== '' ? v : undefined);

function parseEvidence(responsePayload: string | null | undefined): Evidence | null {
  if (!responsePayload) return null;
  try {
    const parsed: unknown = JSON.parse(responsePayload);
    return parsed !== null && typeof parsed === 'object' && !Array.isArray(parsed) ? (parsed as Evidence) : null;
  } catch {
    return null;
  }
}

// The requested value as the operator would read it: a string without its quotes, anything
// else as JSON.
function show(v: unknown): string | undefined {
  if (v === undefined || v === null) return undefined;
  return typeof v === 'string' ? v : JSON.stringify(v);
}

export function sparkplugOutcome(command: {
  name: string;
  status: string;
  responsePayload?: string | null;
}): SparkplugOutcomeView {
  const none: SparkplugOutcomeView = { outcome: null, secondaryHost: false };
  if (!isSparkplugCommand(command.name)) return none;

  if (command.status === 'SENT') return { outcome: { kind: 'inFlight' }, secondaryHost: false };

  // Everything else is read from the adapter's machine-readable `reason`, never from its error
  // wording. A code this console does not know, or no code (an older adapter), says nothing extra.
  const ev = parseEvidence(command.responsePayload);
  const reason = str(ev?.reason);
  if (!ev || !reason) return none;

  if (command.status === 'TIMEOUT') {
    const kind =
      reason === 'unconfirmed_timeout'
        ? 'unconfirmed'
        : reason === 'superseded'
          ? 'superseded'
          : reason === 'session_dropped'
            ? 'likelyRefused'
            : null;
    return { outcome: kind ? { kind } : null, secondaryHost: false };
  }
  if (command.status === 'FAILED') {
    return { outcome: isRefusalReason(reason) ? { kind: 'refused', reason } : null, secondaryHost: false };
  }
  if (command.status !== 'SUCCESSFUL') return none;

  const secondaryHost = ev.hostRole === 'secondary';
  const via = str(ev.via);
  if (reason === 'unchanged') {
    return {
      outcome: { kind: 'alreadyAtValue', metric: str(ev.metric), value: show(ev.value), since: str(ev.since) },
      secondaryHost,
    };
  }
  if (reason !== 'confirmed' || !via) return { outcome: null, secondaryHost };
  if (ev.kind === 'rebirth' || ev.kind === 'reboot') {
    return { outcome: { kind: 'confirmedByBirth', at: str(ev.reportedAt) }, secondaryHost };
  }
  return {
    outcome: { kind: 'confirmedByReport', metric: str(ev.metric), value: show(ev.value), at: str(ev.reportedAt), via },
    secondaryHost,
  };
}
