// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
import '@/i18n/lazy/alarmMaintenance';
import { useTranslation } from 'react-i18next';
import { FormField } from '@/components/ui/form-field';
import { Combobox } from '@/components/ui/combobox';
import { SEVERITIES } from './rules';

export function SeverityField({ value, onChange }: { value: string; onChange: (value: string) => void }) {
  const { t } = useTranslation('alarmMaintenance');
  return <FormField label={t('bypass')}><Combobox value={value} onChange={onChange}
    placeholder={t('noBypass')} options={SEVERITIES.map(s => ({ value: s, label: t(`severity${s}`) }))} /></FormField>;
}
