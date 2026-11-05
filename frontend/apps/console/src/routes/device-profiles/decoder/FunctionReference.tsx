// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The function library, served by the platform (so it cannot drift from what the compiler
// accepts): every function an expression may call, with its signature and limits.

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Input } from '@/components/ui/input';
import { LoadingState } from '@/components/ui/loading-state';
import { useQuery } from '@/lib/hooks/use-query';
import { listPayloadDecoderFunctions } from '@/lib/api/payload-decoder';

export function FunctionReference() {
  const { t } = useTranslation('deviceProfiles');
  const { data, loading, error } = useQuery(listPayloadDecoderFunctions, []);
  const [q, setQ] = useState('');
  const needle = q.trim().toLowerCase();
  const shown = (data ?? []).filter(
    (f) => needle === '' || f.name.toLowerCase().includes(needle) || f.description.toLowerCase().includes(needle),
  );

  return (
    <aside aria-label={t('decFunctionsTitle')} className="space-y-3">
      <h3 className="text-sm font-semibold">{t('decFunctionsTitle')}</h3>
      <p className="text-xs text-muted-foreground">{t('decFunctionsHint')}</p>
      <Input
        type="search"
        aria-label={t('decFunctionsFilter')}
        placeholder={t('decFunctionsFilter')}
        value={q}
        onChange={(e) => setQ(e.target.value)}
      />
      {loading && !data ? (
        <LoadingState description={t('decFunctionsLoading')} />
      ) : error && !data ? (
        <p role="alert" className="text-sm text-destructive">{error}</p>
      ) : shown.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t('decFunctionsNone')}</p>
      ) : (
        <ul className="max-h-[32rem] space-y-3 overflow-y-auto pr-1">
          {shown.map((f) => (
            <li key={f.name + f.signature}>
              <code className="block break-all font-mono text-xs font-semibold">{f.signature}</code>
              <p className="text-xs text-muted-foreground">{f.description}</p>
            </li>
          ))}
        </ul>
      )}
    </aside>
  );
}
