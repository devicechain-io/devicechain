// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useId, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/button';
import { FormField } from '@/components/ui/form-field';
import { Input } from '@/components/ui/input';
import { Select, SelectItem } from '@/components/ui/select';
import { buildCelPreset, type CelContext, type CelPreset } from './cel-presets';
import '@/i18n/lazy/deviceProfiles';

/** Applying is explicit: opening a saved CEL expression must never rewrite it. */
export function CelPresetFields({ context, math = false, onApply }: { context: CelContext; math?: boolean; onApply: (cel: string) => void }) {
  const { t } = useTranslation('deviceProfiles');
  const id = useId();
  const [mathContext, setMathContext] = useState<CelContext>(context);
  const [kind, setKind] = useState<CelPreset>(math ? 'math' : 'filter');
  const [metric, setMetric] = useState('');
  const [op, setOp] = useState(math ? '*' : '>');
  const [number, setNumber] = useState('');
  const selectedContext = math ? mathContext : context;
  const expression = buildCelPreset(kind, selectedContext, metric, op, number);
  return (
    <details className="space-y-3 rounded-md border border-dashed p-3">
      <summary>{t('celPresetSummary')}</summary>
      {math && <FormField label={t('celPresetSource')} htmlFor={`${id}-source`}>
        <Select id={`${id}-source`} value={mathContext} onChange={(v) => setMathContext(v as CelContext)}>
          <SelectItem value="event">{t('celPresetEvent')}</SelectItem>
          <SelectItem value="detection">{t('celPresetDetection')}</SelectItem>
        </Select>
      </FormField>}
      {!math && <FormField label={t('celPresetKind')} htmlFor={`${id}-kind`}>
        <Select id={`${id}-kind`} value={kind} onChange={(v) => setKind(v as CelPreset)}>
          <SelectItem value="filter">{t('celPresetFilter')}</SelectItem>
          <SelectItem value="exists">{t('celPresetExists')}</SelectItem>
        </Select>
      </FormField>}
      {selectedContext === 'event' && <FormField label={t('inspectorMetricLabel')} htmlFor={`${id}-metric`}>
        <Input id={`${id}-metric`} value={metric} onChange={(e) => setMetric(e.target.value)} />
      </FormField>}
      {kind !== 'exists' && <>
        <FormField label={t('inspectorOpLabel')} htmlFor={`${id}-op`}>
          <Select id={`${id}-op`} value={op} onChange={setOp}>
            {(math ? ['+', '-', '*', '/'] : ['>', '>=', '<', '<=', '==', '!=']).map((o) => <SelectItem key={o} value={o}>{o}</SelectItem>)}
          </Select>
        </FormField>
        <FormField label={t('inspectorValueLabel')} htmlFor={`${id}-number`}>
          <Input id={`${id}-number`} type="number" value={number} onChange={(e) => setNumber(e.target.value)} />
        </FormField>
      </>}
      {expression && <pre className="overflow-auto text-xs">{expression}</pre>}
      <Button type="button" disabled={expression === null} onClick={() => { if (expression !== null) onApply(expression); }}>{t('celPresetApply')}</Button>
    </details>
  );
}
