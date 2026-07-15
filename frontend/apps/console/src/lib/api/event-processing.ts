// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the event-processing service. Its one console-facing
// surface is the DETECT rule compiler validation gate (ADR-051 / ADR-044): the console
// authoring form calls it to show a rule's compile/cost/type diagnostics inline BEFORE the
// profile publish re-validates the frozen snapshot authoritatively. It is the single-homed
// authority for rule structure — device-management stores the rule blob opaquely and never
// parses the taxonomy, so this is where a bad rule is actually caught.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/event-processing';
import type { RuleHealthQuery, DetectionStreamSubscription, CompileCanvasQuery, PreviewRuleQuery } from '@/gql/event-processing/graphql';

const VALIDATE_DETECTION_RULES = graphql(`
  query ValidateDetectionRules($rules: [DetectionRuleInput!]!) {
    validateDetectionRules(rules: $rules) {
      valid
      errors {
        index
        token
        message
      }
    }
  }
`);

// The outcome of validating one rule: ok, or the compiler's console-surfaceable reason.
export interface RuleValidation {
  ok: boolean;
  message: string | null;
}

// validateDetectionRule compiles + cost-gates a single rule definition through the batch
// gate and collapses the result to one diagnostic. `token` anchors the compiler's error
// messages; the definition is the opaque rules.Rule JSON string the form emits. It is pure
// validation (no state is read or written), so it is safe to call repeatedly / debounced.
export async function validateDetectionRule(
  token: string,
  definition: string,
  groupScoped = false,
): Promise<RuleValidation> {
  const data = await gql('event-processing', VALIDATE_DETECTION_RULES, {
    // groupScoped lets the gate reject a scope on an unsupported kind (absence/correlation)
    // inline (ADR-062 S4), so the author sees it in the editor, not only at publish.
    rules: [{ token, definition, groupScoped }],
  });
  const result = data.validateDetectionRules;
  if (result.valid) return { ok: true, message: null };
  return { ok: false, message: result.errors[0]?.message ?? 'The rule did not compile.' };
}

// ── Canvas compile (ADR-053 slice 9b) ─────────────────────────────────────
// The server-authoritative graph→schema pass: the console editor sends its opaque
// CanvasDefinition JSON and gets back the compiled rules.Rule definition (byte-identical
// to the equivalent form rule), a predicate cost estimate, and node-anchored diagnostics.
// The browser NEVER authors the definition directly — it holds the graph and shows
// diagnostics on nodes; this is the only place a canvas becomes a runnable rule.

const COMPILE_CANVAS = graphql(`
  query CompileCanvas($graph: String!, $profileToken: String!) {
    compileCanvas(graph: $graph, profileToken: $profileToken) {
      ok
      definition
      estimatedCost
      diagnostics {
        nodeId
        severity
        message
      }
    }
  }
`);

export type CanvasCompileResult = CompileCanvasQuery['compileCanvas'];
export type CanvasDiagnostic = CanvasCompileResult['diagnostics'][number];

// compileCanvas lowers a CanvasDefinition (opaque JSON string) to its DETECT rule definition,
// scoped to the profile. It never throws for a compile rejection — that comes back as
// ok:false + diagnostics; it throws only on a transport/auth failure (which the caller may
// swallow to degrade to no-feedback, like the form's inline validation).
export async function compileCanvas(graph: string, profileToken: string): Promise<CanvasCompileResult> {
  const data = await gql('event-processing', COMPILE_CANVAS, { graph, profileToken });
  return data.compileCanvas;
}

// ── Replay preview (ADR-053 slice 9d — the headline) ──────────────────────
// Run the DRAFT rule (the current canvas graph) against replayed history and show the
// RAISE/RESOLVE edges it WOULD have produced over a window — "what would this rule have done?".
// The server builds a throwaway in-memory DETECT core over an ephemeral replay consumer and
// collects edges instead of publishing, so it never perturbs the live engine. Compile failures
// come back as ok:false + diagnostics; an unpublished profile / aged-out window / scan cap comes
// back as a degraded (but non-error) result.

// The trace sub-selection (slice 9e) — what each canvas node did for a firing, for the canvas
// overlay. Requested only when input.trace is set; empty on every firing otherwise.
const PREVIEW_RULE = graphql(`
  query PreviewRule($input: PreviewRuleInput!) {
    previewRule(input: $input) {
      ok
      firings {
        occurredAt
        series
        signal
        trace {
          nodeId
          kind
          disposition
          detail
        }
      }
      stats {
        eventsScanned
        firingCount
        evalErrors
        wallMs
      }
      degraded
      diagnostics {
        nodeId
        severity
        message
      }
    }
  }
`);

export type PreviewResult = PreviewRuleQuery['previewRule'];
export type PreviewFiring = PreviewResult['firings'][number];
export type NodeTraceStep = PreviewFiring['trace'][number];

// previewRule runs a draft (a canvas graph OR a rules.Rule definition) against history over an
// RFC3339 window. It throws only on transport/auth failure; a compile rejection or a degraded run
// comes back in the result. Exactly one of graph/ruleDefinition should be set. Set trace to attach a
// per-firing node trace (canvas graph only — a ruleDefinition draft has no canvas node map).
export async function previewRule(input: {
  graph?: string;
  ruleDefinition?: string;
  profileToken: string;
  start: string;
  end: string;
  trace?: boolean;
}): Promise<PreviewResult> {
  const data = await gql('event-processing', PREVIEW_RULE, { input });
  return data.previewRule;
}

// ── Rule health (ADR-051 slice 7b/7c) ─────────────────────────────────────
// Per-rule status / last-fired / fire-count for a profile's ACTIVE published version.

export type RuleHealth = RuleHealthQuery['ruleHealth'][number];

const RULE_HEALTH = graphql(`
  query RuleHealth($profileToken: String!) {
    ruleHealth(profileToken: $profileToken) {
      ruleId
      ruleToken
      name
      status
      lastFiredAt
      fireCount
      lastSignal
      message
    }
  }
`);

export async function listRuleHealth(profileToken: string): Promise<RuleHealth[]> {
  const data = await gql('event-processing', RULE_HEALTH, { profileToken });
  return data.ruleHealth;
}

// ── Live detection feed (ADR-037 / ADR-057 slice 7c) ──────────────────────
// The stream of a profile's DETECT firings (raised/resolved), consumed by
// useDetectionStream. Best-effort delivery: it is a live feed, not a source of
// truth (ruleHealth's fire counts are the durable record) — a consumer treats
// each event as an append to a capped recent-firings list and re-runs ruleHealth
// on reconnect. Exported (unlike the query documents) because the hook passes it
// to the SDK subscribe().
export type DetectionStreamData = DetectionStreamSubscription;
export type DetectionEvent = DetectionStreamSubscription['detectionStream'];

export const DETECTION_STREAM = graphql(`
  subscription DetectionStream($profileToken: String!) {
    detectionStream(profileToken: $profileToken) {
      ruleId
      ruleToken
      kind
      edge
      series
      occurredTime
      severity
      value
    }
  }
`);
