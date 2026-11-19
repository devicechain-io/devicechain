// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
import '@/i18n/lazy/alarmMaintenance';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';

/** Keep each key verbatim: commas and surrounding whitespace can belong to a key. */
export function AlarmKeysEditor({ keys, onChange }: { keys: string[]; onChange: (keys: string[]) => void }) {
  const { t } = useTranslation('alarmMaintenance');
  return <fieldset className="space-y-2">
    <legend>{t('alarmKeys')}</legend>
    <p className="text-sm text-muted-foreground">{t('keysHint')}</p>
    {keys.map((key, index) => <div key={index} className="flex gap-2">
      <Input aria-label={t('alarmKeyRow', { index: index + 1 })} value={key}
        onChange={e => onChange(keys.map((value, i) => i === index ? e.target.value : value))} />
      <Button type="button" variant="ghost" onClick={() => onChange(keys.filter((_, i) => i !== index))}>{t('removeKey')}</Button>
    </div>)}
    <Button type="button" variant="outline" disabled={keys.length >= 20} onClick={() => onChange([...keys, ''])}>{t('addKey')}</Button>
  </fieldset>;
}
