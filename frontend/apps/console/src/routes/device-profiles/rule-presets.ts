// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Reviewed alert PRESETS: the three alerts an operator asks for first, as pure functions from
// (the profile's declared metrics, a few parameters) to the SAME rules.Rule definition JSON the
// detection-rule form already emits. A preset is not a second editor and not a second taxonomy —
// it is a starting point handed to the ordinary form (the `initialDefinition` seam the
// "Describe" door uses), so the operator lands in the one editor, with the one compiler gate and
// the one draft/publish path, and every field stays editable.
//
// The definitions are built through buildDefinition, so a preset cannot emit a shape the form
// cannot round-trip. Their compile-ability is pinned against the backend compiler by the golden
// files under event-processing/internal/rules/testdata/presets (compiled by a Go test, compared
// here by rule-presets.test.ts).
//
// Deliberate limits: nothing is guessed silently. A preset that needs a metric the profile does
// not clearly offer leaves the field empty for the form to ask for; a threshold with no sensible
// default (held threshold) is left unset rather than filled with a plausible number.

import { buildDefinition, type BuildArgs, type RuleType } from './rule-model';

export const PRESET_IDS = ['deviceSilent', 'heldThreshold', 'lowBattery'] as const;
export type PresetId = (typeof PRESET_IDS)[number];

/** Metric names that look like a battery level, best match first. */
export const BATTERY_METRIC_PATTERN = /battery|batt|soc/i;

export interface PresetParams {
  /** The profile's declared metric keys, in declaration order. */
  metrics: readonly string[];
  /** Held threshold only: the operator's bound, when already known. Never defaulted. */
  threshold?: number;
}

export interface PresetResult {
  /** The rules.Rule definition JSON to hand to the form as `initialDefinition`. */
  definition: string;
  /** The rule type, for callers that need to say what they are about to open. */
  ruleType: RuleType;
  /** True when the operator must still pick/confirm the metric (it was not clearly resolved). */
  metricToConfirm: boolean;
}

// Documented values. Exported so the picker's copy and the tests read the same numbers.
export const SILENT_TIMEOUT = '15m';
export const HELD_HOLD = '10m';
export const BATTERY_HOLD = '10m';
export const BATTERY_LOW_BELOW = 20;

/** The first declared metric whose name looks like a battery level, or null. */
export function pickBatteryMetric(metrics: readonly string[]): string | null {
  const hits = metrics.filter((m) => BATTERY_METRIC_PATTERN.test(m));
  // A charge level beats a voltage reading: "< 20" is a percentage, not a volt.
  return hits.find((m) => /level|pct|percent|soc|charge/i.test(m)) ?? hits.find((m) => !/volt|(^|_)v$/i.test(m)) ?? hits[0] ?? null;
}

const base: BuildArgs = {
  definitionName: '',
  description: '',
  severity: '',
  ruleType: 'duration',
  condMode: 'structured',
  condMetric: '',
  condOp: 'gt',
  boundKind: 'literal',
  condThreshold: '',
  condAttr: '',
  cel: '',
  valueMetric: '',
  aggFunc: 'avg',
  windowMode: 'tumbling',
  windowStr: '',
  holdStr: '',
  timeoutStr: '',
  gapStr: '',
  countStr: '',
  rate: false,
  aggOp: 'gt',
  aggThreshold: '',
  anchorType: '',
  memberCapStr: '',
  actions: [],
};

const raise = (alarmKey: string) => [{ type: 'raiseAlarm' as const, alarmKey, command: '', payload: '' }];

/**
 * Builds a preset's definition. `name`/`description` are the user-facing strings (the caller
 * supplies the translation; the preset module holds no i18n).
 */
export function buildPreset(id: PresetId, params: PresetParams, text: { name: string; description: string }): PresetResult {
  switch (id) {
    case 'deviceSilent': {
      // absence: timer-driven off the profile roster, so it fires for a device that has gone quiet
      // and resolves on its next event. It takes no condition and (deliberately) no group scope.
      const definition = buildDefinition({
        ...base,
        definitionName: text.name,
        description: text.description,
        severity: 'major',
        ruleType: 'absence',
        condMode: 'none',
        timeoutStr: SILENT_TIMEOUT,
        actions: raise('device-silent'),
      });
      return { definition, ruleType: 'absence', metricToConfirm: false };
    }
    case 'heldThreshold': {
      // duration: the condition must hold continuously for HELD_HOLD. The metric is the operator's
      // call: pre-filled only when the profile declares exactly one, otherwise left to choose.
      const metric = params.metrics.length === 1 ? params.metrics[0] : '';
      const definition = withoutUnsetThreshold(
        buildDefinition({
          ...base,
          definitionName: text.name,
          description: text.description,
          severity: 'major',
          ruleType: 'duration',
          condMetric: metric,
          condOp: 'gt',
          condThreshold: params.threshold === undefined ? '' : String(params.threshold),
          holdStr: HELD_HOLD,
          actions: raise('threshold-held'),
        }),
        params.threshold === undefined,
      );
      return { definition, ruleType: 'duration', metricToConfirm: true };
    }
    case 'lowBattery': {
      // duration: the level stays below BATTERY_LOW_BELOW for BATTERY_HOLD. The battery metric is
      // PREFERRED by name but always flagged for confirmation — a wrong guess would watch the
      // wrong signal and look like a working alert.
      const metric = pickBatteryMetric(params.metrics) ?? '';
      const definition = buildDefinition({
        ...base,
        definitionName: text.name,
        description: text.description,
        severity: 'warning',
        ruleType: 'duration',
        condMetric: metric,
        condOp: 'lt',
        condThreshold: String(BATTERY_LOW_BELOW),
        holdStr: BATTERY_HOLD,
        actions: raise('low-battery'),
      });
      return { definition, ruleType: 'duration', metricToConfirm: true };
    }
  }
}

// buildDefinition writes a missing number as 0, which is a plausible value for a bound the
// operator has not chosen. Drop the key instead so the form opens with the field empty and its
// own "needs a threshold" hint fires.
function withoutUnsetThreshold(definition: string, unset: boolean): string {
  if (!unset) return definition;
  const d = JSON.parse(definition) as { when?: Record<string, unknown> };
  if (d.when) delete d.when.threshold;
  return JSON.stringify(d);
}
