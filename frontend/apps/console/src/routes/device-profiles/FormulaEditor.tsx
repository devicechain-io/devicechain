// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The formula field of a calculated metric, with the server's own verdict beside it.
//
// Nothing here parses or evaluates a formula. Every keystroke (debounced) goes to the
// testMetricFormula query, which compiles it with the same code the publish gate and the
// resolver run, so what this panel shows is what would be admitted and what would be
// computed — never a second implementation that could drift. The sample is hand-typed
// values for the measured metrics the formula reads; nothing is stored. Below it,
// FormulaHistory runs the same draft over a device's stored readings (calculatedEvents).

import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Input } from '@/components/ui/input';
import { Textarea } from '@/components/ui/textarea';
import { FormField } from '@/components/ui/form-field';
import { HintText } from '@/components/ui/hint-text';
import { errMessage } from '@/routes/common';
import { testMetricFormula, type MetricFormulaTest } from '@/lib/api/device-management';
import { FormulaHistory } from './FormulaHistory';
import { optNum } from '@/lib/utils';

/** The per-formula cost ceiling the publish gate enforces (the API states it in its schema). */
export const FORMULA_COST_CEILING = 100;

/** Most calculated metrics in one chain (the publish gate refuses a longer one). */
export const FORMULA_CHAIN_DEPTH = 4;

/** Longest formula the API accepts, in bytes. */
export const FORMULA_MAX_BYTES = 1024;

const DEBOUNCE_MS = 500;

/** The skip reasons the evaluator reports; anything else is shown as the raw code. */
const SKIP_REASONS = [
  'missing_input',
  'invalid_input',
  'non_finite',
  'div_zero',
  'overflow',
  'range',
  'unstorable',
  'eval_error',
];

export function FormulaEditor({
  profileToken,
  metricKey,
  dataType,
  minValue,
  maxValue,
  expression,
  onChange,
}: {
  profileToken: string;
  metricKey: string;
  dataType: string;
  minValue: string;
  maxValue: string;
  expression: string;
  onChange: (expression: string) => void;
}) {
  const { t } = useTranslation('deviceProfiles');
  const [sample, setSample] = useState<Record<string, string>>({});
  // The hand-typed values of the device constants the formula reads (`device.name`): the numbers
  // a device's metadata would hold. Sent with the sample; nothing is stored.
  const [constants, setConstants] = useState<Record<string, string>>({});
  const [result, setResult] = useState<MetricFormulaTest | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const [checking, setChecking] = useState(false);
  // Only the newest request may write: a slow answer for an earlier draft must not overwrite
  // the verdict on the current one.
  const latest = useRef(0);

  const trimmed = expression.trim();
  const tooLong = new TextEncoder().encode(expression).length > FORMULA_MAX_BYTES;
  const sampleKey = JSON.stringify(sample);
  const constantsKey = JSON.stringify(constants);

  useEffect(() => {
    if (!trimmed || tooLong) {
      latest.current++;
      setResult(null);
      setFailure(null);
      setChecking(false);
      return;
    }
    const mine = ++latest.current;
    setChecking(true);
    const timer = setTimeout(async () => {
      try {
        const min = optNum(minValue);
        const max = optNum(maxValue);
        const answer = await testMetricFormula({
          profileToken,
          expression: trimmed,
          dataType,
          metricKey: metricKey.trim() || null,
          minValue: min,
          maxValue: max,
          sample: Object.entries(JSON.parse(sampleKey) as Record<string, string>)
            .filter(([, v]) => v.trim() !== '')
            .map(([key, value]) => ({ key, value: value.trim() })),
          constants: Object.entries(JSON.parse(constantsKey) as Record<string, string>)
            .filter(([, v]) => v.trim() !== '')
            .map(([key, value]) => ({ key, value: value.trim() })),
        });
        if (mine !== latest.current) return;
        setResult(answer);
        setFailure(null);
      } catch (err) {
        if (mine !== latest.current) return;
        setResult(null);
        setFailure(errMessage(err));
      } finally {
        if (mine === latest.current) setChecking(false);
      }
    }, DEBOUNCE_MS);
    return () => clearTimeout(timer);
  }, [profileToken, trimmed, tooLong, dataType, metricKey, minValue, maxValue, sampleKey, constantsKey]);

  const inputs = result?.compileError ? [] : (result?.inputs ?? []);
  // A formula may read other calculated metrics. The sample supplies the MEASURED metrics the whole
  // chain reads (the server computes the calculated ones from them, in order, as it does when a
  // reading arrives), and the chain is shown so the author can see what the formula rests on.
  const chain = result?.compileError ? [] : (result?.chain ?? []);
  const sampleInputs = result?.compileError ? [] : result?.measuredInputs?.length ? result.measuredInputs : inputs;
  const constantNames = result?.compileError ? [] : (result?.constants ?? []);
  const cost = result?.estimatedCost ?? null;

  return (
    <div className="space-y-3">
      <FormField
        label={t('formulaLabel')}
        htmlFor="m-expression"
        description={t('formulaHint', { max: FORMULA_MAX_BYTES })}
      >
        <Textarea
          id="m-expression"
          className="font-mono"
          value={expression}
          onChange={(e) => onChange(e.target.value)}
          placeholder={t('formulaPlaceholder')}
          spellCheck={false}
        />
      </FormField>

      {tooLong && (
        <p role="alert" className="text-sm text-destructive">
          {t('formulaTooLong', { max: FORMULA_MAX_BYTES })}
        </p>
      )}
      {failure && (
        <p role="alert" className="text-sm text-destructive">
          {t('formulaCheckFailed', { message: failure })}
        </p>
      )}
      {checking && <HintText size="md">{t('formulaChecking')}</HintText>}
      {result?.compileError && (
        <p role="alert" className="text-sm text-destructive">
          {t('formulaCompileError', { message: result.compileError })}
        </p>
      )}
      {result && !result.compileError && (
        <div className="space-y-3 rounded-md border p-3" data-testid="formula-result">
          <p className="text-sm">
            {t('formulaCompiles')}
            {cost != null && (
              <span className="text-muted-foreground">
                {' · '}
                {t('formulaCost', { cost, ceiling: FORMULA_COST_CEILING })}
              </span>
            )}
          </p>
          {result.warnings.map((w) => (
            <p key={w} className="text-sm text-warning">
              {w}
            </p>
          ))}
          {chain.length > 0 && (
            <div className="space-y-1" data-testid="formula-chain">
              <p className="text-sm font-medium">{t('formulaChainTitle')}</p>
              <p className="font-mono text-sm">{[...chain, metricKey.trim() || t('formulaChainThis')].join(' → ')}</p>
              <HintText size="md">{t('formulaChainHint', { max: FORMULA_CHAIN_DEPTH })}</HintText>
            </div>
          )}
          {inputs.length > 0 && (
            <div className="space-y-2">
              <p className="text-sm font-medium">{t('formulaSampleTitle')}</p>
              <HintText size="md">{t('formulaSampleHint')}</HintText>
              <div className="grid grid-cols-1 gap-2 sm:grid-cols-3">
                {sampleInputs.map((key) => (
                  <FormField key={key} label={key} htmlFor={`m-sample-${key}`}>
                    <Input
                      id={`m-sample-${key}`}
                      value={sample[key] ?? ''}
                      onChange={(e) => setSample((s) => ({ ...s, [key]: e.target.value }))}
                      inputMode="decimal"
                    />
                  </FormField>
                ))}
              </div>
              {constantNames.length > 0 && (
                <div className="space-y-2" data-testid="formula-constants">
                  <p className="text-sm font-medium">{t('formulaConstantsTitle')}</p>
                  <HintText size="md">{t('formulaConstantsHint')}</HintText>
                  <div className="grid grid-cols-1 gap-2 sm:grid-cols-3">
                    {constantNames.map((name) => (
                      <FormField key={name} label={`device.${name}`} htmlFor={`m-constant-${name}`}>
                        <Input
                          id={`m-constant-${name}`}
                          value={constants[name] ?? ''}
                          onChange={(e) => setConstants((s) => ({ ...s, [name]: e.target.value }))}
                          inputMode="decimal"
                        />
                      </FormField>
                    ))}
                  </div>
                </div>
              )}
              <p className="text-sm" data-testid="formula-outcome">
                {result.value != null
                  ? t('formulaValue', { value: result.value })
                  : result.skippedReason != null
                    ? t('formulaSkipped', { reason: skipLabel(t, result.skippedReason) })
                    : null}
              </p>
              <FormulaHistory
                profileToken={profileToken}
                expression={trimmed}
                dataType={dataType}
                minValue={minValue}
                maxValue={maxValue}
                inputs={inputs}
                constants={constantNames}
                skipLabel={(reason) => skipLabel(t, reason)}
              />
            </div>
          )}
        </div>
      )}
    </div>
  );
}

export function skipLabel(t: (key: string) => string, reason: string): string {
  return SKIP_REASONS.includes(reason) ? t(`formulaSkip.${reason}`) : reason;
}
