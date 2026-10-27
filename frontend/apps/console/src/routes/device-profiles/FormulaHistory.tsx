// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// "Try it on recent history": the draft formula run over one device's stored readings.
//
// Like the hand-typed sample above it, nothing here evaluates a formula: the calculatedEvents
// query compiles the draft with the same code the resolver runs and evaluates it over each stored
// sample, returning every sample's inputs and value (or why it produced none) and a tally by
// reason. Nothing is stored. The inputs are the STORED values, kept to a fixed number of decimal
// places, which is why the panel always says so: a value here is not promised to be the value
// that will be stored for a reading, which is computed from the device's full-precision value.

import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { errorCode } from '@devicechain/client';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { HintText } from '@/components/ui/hint-text';
import { SegmentedControl } from '@/components/ui/segmented-control';
import { errMessage } from '@/routes/common';
import { getDevice, listMetricDefinitions } from '@/lib/api/device-management';
import { calculatedEvents, type CalculatedEventsResult } from '@/lib/api/event-management';
import { optNum } from '@/lib/utils';
import { absentInputs } from './calcHealth';

/** The most samples the server evaluates in one request (it refuses above this). */
export const HISTORY_SAMPLE_CAP = 50_000;

/** How many of the newest samples are listed; the tally covers all of them. */
export const HISTORY_ROWS_SHOWN = 50;

// Window presets, in hours back from now. Plain data, so the lint reads no literal text here.
const WINDOWS: { hours: number }[] = [{ hours: 1 }, { hours: 24 }, { hours: 168 }];

type State =
  | { status: 'idle' }
  | { status: 'running' }
  | { status: 'done'; result: CalculatedEventsResult }
  | { status: 'error'; message: string };

export function FormulaHistory({
  profileToken,
  expression,
  dataType,
  minValue,
  maxValue,
  inputs,
  constants,
  skipLabel,
  inputTypes,
}: {
  profileToken: string;
  expression: string;
  dataType: string;
  minValue: string;
  maxValue: string;
  /** The metric keys the formula reads directly (measured or calculated), as the server reported them. */
  inputs: string[];
  /** The device constants the formula reads (`device.name`), read from the device's metadata. */
  constants: string[];
  skipLabel: (reason: string) => string;
  /**
   * The data type of each metric the formula may read, when the caller holds the version the
   * formula belongs to. Omitted, the types are the profile's draft metrics', which is right for
   * the draft formula being written and wrong for a published one the draft has diverged from.
   */
  inputTypes?: Record<string, string>;
}) {
  const { t } = useTranslation('deviceProfiles');
  const [deviceToken, setDeviceToken] = useState('');
  const [hours, setHours] = useState(24);
  const [state, setState] = useState<State>({ status: 'idle' });
  // Only the newest run may write, and an edit to the formula drops a shown result: it described
  // another formula.
  const runToken = useRef(0);
  const formulaKey = JSON.stringify([expression, dataType, minValue, maxValue, inputs, constants]);
  useEffect(() => {
    runToken.current++;
    setState((s) => (s.status === 'idle' ? s : { status: 'idle' }));
  }, [formulaKey]);

  const run = async () => {
    const mine = ++runToken.current;
    setState({ status: 'running' });
    try {
      // The inputs' types come from the profile's draft metrics: the formula compiled against
      // them, so each key it reads is a metric there. A calculated one is read from the value
      // stored for it at resolve, like a measured one.
      const types = inputTypes
        ? new Map(Object.entries(inputTypes))
        : new Map((await listMetricDefinitions(profileToken)).map((m) => [m.metricKey, m.dataType]));
      // A formula that reads device constants is run with THIS device's constants as its metadata
      // holds them now (the server holds no device records, so the console reads them).
      const held = constants.length > 0 ? await deviceConstants(deviceToken.trim(), constants) : [];
      const to = new Date();
      const from = new Date(to.getTime() - hours * 3_600_000);
      const result = await calculatedEvents({
        deviceToken: deviceToken.trim(),
        expression,
        dataType,
        inputs: inputs.filter((k) => types.has(k)).map((key) => ({ key, dataType: types.get(key) ?? '' })),
        minValue: optNum(minValue),
        maxValue: optNum(maxValue),
        constants: held,
        from: from.toISOString(),
        to: to.toISOString(),
      });
      if (mine === runToken.current) setState({ status: 'done', result });
    } catch (err) {
      if (mine !== runToken.current) return;
      setState({
        status: 'error',
        message:
          errorCode(err) === 'LIMIT_EXCEEDED' ? t('formulaHistoryTooMany', { max: HISTORY_SAMPLE_CAP.toLocaleString() }) : errMessage(err),
      });
    }
  };

  return (
    <div className="space-y-2" data-testid="formula-history">
      <p className="text-sm font-medium">{t('formulaHistoryTitle')}</p>
      <HintText size="md">{t('formulaHistoryHint')}</HintText>
      <div className="flex flex-wrap items-end gap-2">
        <FormField label={t('formulaHistoryDevice')} htmlFor="m-history-device">
          <Input
            id="m-history-device"
            value={deviceToken}
            onChange={(e) => setDeviceToken(e.target.value)}
            placeholder={t('formulaHistoryDevicePlaceholder')}
          />
        </FormField>
        <SegmentedControl
          ariaLabel={t('formulaHistoryWindow')}
          tone="joined"
          value={String(hours)}
          onValueChange={(v) => setHours(Number(v))}
          options={WINDOWS.map((w) => ({ value: String(w.hours), label: t('previewWindowLabel', { hours: w.hours }) }))}
        />
        <Button
          size="sm"
          variant="outline"
          onClick={run}
          loading={state.status === 'running'}
          disabled={!deviceToken.trim() || inputs.length === 0 || state.status === 'running'}
        >
          {t('formulaHistoryRun')}
        </Button>
      </div>
      {state.status === 'error' && (
        <p role="alert" className="text-sm text-destructive">
          {state.message}
        </p>
      )}
      {state.status === 'done' && constants.length > 0 && (
        <p className="text-xs text-warning" data-testid="formula-history-constants">
          {t('formulaHistoryConstants', { names: constants.map((c) => `device.${c}`).join(', ') })}
        </p>
      )}
      {state.status === 'done' && <HistoryOutcome result={state.result} skipLabel={skipLabel} inputs={inputs} />}
    </div>
  );
}

/**
 * The device's current values for the constants a formula reads, as the server's resolver would
 * read them: a JSON number under the name is a constant; a name the metadata holds as anything
 * else is sent as blank text, which the server reads as present and not a number (invalid_input);
 * a name it does not hold is left out (missing_input). A device that does not exist, or whose
 * metadata is not a JSON object, holds none.
 */
async function deviceConstants(deviceToken: string, names: string[]): Promise<{ key: string; value: string }[]> {
  const device = await getDevice(deviceToken);
  let doc: unknown = null;
  try {
    doc = device?.metadata ? JSON.parse(device.metadata) : null;
  } catch {
    doc = null;
  }
  if (doc === null || typeof doc !== 'object' || Array.isArray(doc)) return [];
  const held = doc as Record<string, unknown>;
  return names
    .filter((name) => Object.prototype.hasOwnProperty.call(held, name))
    .map((key) => ({ key, value: typeof held[key] === 'number' && Number.isFinite(held[key]) ? String(held[key]) : '' }));
}

function HistoryOutcome({
  result,
  skipLabel,
  inputs,
}: {
  result: CalculatedEventsResult;
  skipLabel: (reason: string) => string;
  inputs: string[];
}) {
  const { t } = useTranslation('deviceProfiles');
  const total = result.samples.length;
  // Newest first, and only the newest few: a chart-length history is summarized by the tally.
  const shown = result.samples.slice(-HISTORY_ROWS_SHOWN).reverse();
  return (
    <div className="space-y-2" data-testid="formula-history-result">
      <p className="text-xs text-warning" data-testid="formula-history-caveat">
        {t('formulaHistoryCaveat', { places: result.inputDecimalPlaces })}
      </p>
      {total === 0 ? (
        <p className="text-sm text-muted-foreground">{t('formulaHistoryEmpty')}</p>
      ) : (
        <>
          <p className="text-sm" data-testid="formula-history-tally">
            {t('formulaHistoryTally', { count: total, computed: result.computed })}
            {result.skipped.map((s) => (
              <span key={s.reason} className="text-muted-foreground">
                {' · '}
                {t('formulaHistorySkippedCount', { count: s.count, reason: skipLabel(s.reason) })}
              </span>
            ))}
          </p>
          {total > shown.length && (
            <HintText size="md">{t('formulaHistoryShowing', { shown: shown.length, count: total })}</HintText>
          )}
          <ul className="max-h-64 space-y-1 overflow-y-auto text-xs">
            {shown.map((s) => (
              <li key={`${s.eventId}/${s.occurredTime}`} className="flex flex-wrap gap-x-3 font-mono">
                <span className="text-muted-foreground">{new Date(s.occurredTime).toLocaleString()}</span>
                <span>{s.inputs.map((i) => `${i.key}=${i.value}`).join(' ')}</span>
                <span>
                  {'→ '}
                  {s.value != null ? s.value : t('formulaSkipped', { reason: skipLabel(s.skippedReason ?? '') })}
                </span>
                {s.value == null && s.skippedReason === 'missing_input' && (
                  <MissingInputs lacked={absentInputs(inputs, s.inputs)} />
                )}
              </li>
            ))}
          </ul>
        </>
      )}
    </div>
  );
}

/** Names the inputs a skipped sample did not carry; says nothing when it carried them all. */
function MissingInputs({ lacked }: { lacked: string[] }) {
  const { t } = useTranslation('deviceProfiles');
  if (lacked.length === 0) return null;
  return (
    <span className="text-warning" data-testid="formula-history-lacked">
      {t('formulaHistoryLacked', { keys: lacked.join(', ') })}
    </span>
  );
}
