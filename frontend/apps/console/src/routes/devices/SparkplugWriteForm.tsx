// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The form for a `sparkplug.write` command: a metric name and a value, both free text. The
// metric and its datatype belong to the node's birth, which the console does not hold, so
// there is nothing to pick from; the adapter checks the metric against the source's writable
// list and the value against the declared datatype and says why when it refuses. How the
// text becomes a JSON scalar is sparkplugCommands.ts's rule, shown to the operator below.

import { useTranslation } from 'react-i18next';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { HintText } from '@/components/ui/hint-text';
import { SegmentedControl } from '@/components/ui/segmented-control';
import type { SparkplugValueMode, SparkplugWriteErrors } from './sparkplugCommands';

export type SparkplugWriteValues = { metric: string; value: string; mode: SparkplugValueMode };

// Hoisted out of the JSX: these are technical tokens, not user text, and the i18n
// literal-string lint walks every node under a JSX element.
const MODE_AUTO: SparkplugValueMode = 'auto';
const MODE_TEXT: SparkplugValueMode = 'text';
const toMode = (v: string): SparkplugValueMode => (v === MODE_TEXT ? MODE_TEXT : MODE_AUTO);

export const EMPTY_SPARKPLUG_WRITE: SparkplugWriteValues = { metric: '', value: '', mode: 'auto' };

export function SparkplugWriteForm({
  values,
  errors,
  disabled,
  onChange,
}: {
  values: SparkplugWriteValues;
  errors: SparkplugWriteErrors;
  disabled?: boolean;
  onChange: (next: SparkplugWriteValues) => void;
}) {
  const { t } = useTranslation('devices');
  return (
    <div className="space-y-3" data-testid="sparkplug-write-form">
      <div className="grid gap-3 sm:grid-cols-2">
        <FormField label={`${t('sparkplugMetricLabel')} *`} description={t('sparkplugMetricHint')}>
          <Input
            value={values.metric}
            disabled={disabled}
            placeholder={t('sparkplugMetricPlaceholder')}
            aria-label={t('sparkplugMetricLabel')}
            onChange={(e) => onChange({ ...values, metric: e.target.value })}
          />
          {errors.metric && <p className="mt-1 text-xs text-destructive">{t('sparkplugMetricRequired')}</p>}
        </FormField>
        <FormField label={`${t('sparkplugValueLabel')} *`} description={t('sparkplugValueHint')}>
          <Input
            value={values.value}
            disabled={disabled}
            placeholder={t('sparkplugValuePlaceholder')}
            aria-label={t('sparkplugValueLabel')}
            onChange={(e) => onChange({ ...values, value: e.target.value })}
          />
          {errors.value && <p className="mt-1 text-xs text-destructive">{t('sparkplugValueRequired')}</p>}
        </FormField>
      </div>
      <FormField label={t('sparkplugValueModeLabel')}>
        <SegmentedControl
          options={[
            { value: MODE_AUTO, label: t('sparkplugValueModeAuto') },
            { value: MODE_TEXT, label: t('sparkplugValueModeText') },
          ]}
          value={values.mode}
          onValueChange={(v) => onChange({ ...values, mode: toMode(v) })}
          ariaLabel={t('sparkplugValueModeLabel')}
          disabled={disabled}
        />
      </FormField>
      <HintText>{t('sparkplugWriteHint')}</HintText>
    </div>
  );
}
