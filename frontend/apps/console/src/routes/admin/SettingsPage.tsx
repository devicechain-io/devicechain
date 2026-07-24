// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { generateToken } from '@devicechain/client';
import { PageShell } from '@/components/ui/page-shell';
import { SectionPanel } from '@/components/ui/section-panel';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import { ErrorBanner } from '@/components/ui/error-banner';
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useQuery } from '@/lib/hooks/use-query';
import { listSettings, setSetting, clearSetting, type Setting } from '@/lib/api/settings';
import { Textarea, useReload, errMessage } from '@/routes/common';

// The token-masks setting gets a live preview of what each entity type's mask
// generates (ADR-042 P3), so an admin sees the effect of an edit immediately.
const TOKEN_MASKS_KEY = 'entity.token_masks';

// The fixed seed the mask preview samples from. Not user-facing data — it exists
// only to produce a deterministic illustrative token — but it is quoted inside
// the preview label, so it is interpolated rather than baked into the catalog
// string (see maskPreviewLabel).
const SAMPLE_SEED = 'Sample Name';

// pretty renders a stored JSON value multi-line for editing; a value that somehow
// does not parse is shown verbatim rather than lost.
function pretty(json: string): string {
  try {
    return JSON.stringify(JSON.parse(json), null, 2);
  } catch {
    return json;
  }
}

// MaskPreview shows a sample generated token per entity type for the current
// (possibly unsaved) token-masks JSON. Purely illustrative — it re-samples only
// when the JSON changes.
function MaskPreview({ json }: { json: string }) {
  const { t } = useTranslation('adminSettings');
  const entries = useMemo(() => {
    let masks: unknown;
    try {
      masks = JSON.parse(json);
    } catch {
      return null;
    }
    if (!masks || typeof masks !== 'object' || Array.isArray(masks)) return null;
    return Object.entries(masks as Record<string, unknown>)
      .filter(([, mask]) => typeof mask === 'string')
      .map(([type, mask]) => [type, generateToken(mask as string, { seed: SAMPLE_SEED })] as const);
  }, [json]);

  if (!entries || entries.length === 0) return null;
  return (
    <div className="rounded-md border border-border bg-muted/30 p-3 text-xs">
      <p className="mb-2 font-medium text-muted-foreground">
        {t('maskPreviewLabel', { seed: SAMPLE_SEED })}
      </p>
      <ul className="space-y-1 font-mono">
        {entries.map(([type, sample]) => (
          <li key={type} className="flex gap-2">
            <span className="w-32 shrink-0 truncate text-muted-foreground">{type}</span>
            <span className="text-muted-foreground">→</span>
            <span className="text-foreground">{sample}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}

export default function SettingsPage() {
  const { t } = useTranslation('adminSettings');
  const [version, reload] = useReload();
  const { data: settings, loading, error } = useQuery(listSettings, [version]);

  return (
    <PageShell title={t('title')} description={t('description')}>
      {loading ? (
        <LoadingState description={t('loadingSettings')} />
      ) : error ? (
        <ErrorState description={error} />
      ) : !settings || settings.length === 0 ? (
        <EmptyState description={t('noSettingsDefined')} />
      ) : (
        <div className="space-y-6">
          {settings.map((s) => (
            // Re-seed the editor when the stored value changes (after a save or a
            // reset) by keying on it.
            <SettingCard key={`${s.key}::${s.value}`} setting={s} onChanged={reload} />
          ))}
        </div>
      )}
    </PageShell>
  );
}

function SettingCard({ setting, onChanged }: { setting: Setting; onChanged: () => void }) {
  const { t } = useTranslation('adminSettings');
  const { toast } = useToast();
  const confirm = useConfirm();
  const [value, setValue] = useState(() => pretty(setting.value));
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  const dirty = value !== pretty(setting.value);

  const save = async () => {
    setFormError(null);
    let compact: string;
    try {
      compact = JSON.stringify(JSON.parse(value));
    } catch {
      setFormError(t('valueMustBeJsonError'));
      return;
    }
    setBusy(true);
    try {
      await setSetting(setting.key, compact);
      toast(t('settingSavedToast', { key: setting.key }));
      onChanged();
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const reset = async () => {
    if (
      !(await confirm({
        title: t('resetConfirmTitle'),
        description: t('resetConfirmDescription', { key: setting.key }),
        confirmLabel: t('resetConfirmLabel'),
      }))
    )
      return;
    setBusy(true);
    setFormError(null);
    try {
      await clearSetting(setting.key);
      toast(t('settingResetToast', { key: setting.key }));
      onChanged();
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <SectionPanel
      title={setting.key}
      description={setting.description}
      action={
        setting.overridden ? (
          <Badge variant="default">{t('overriddenBadge')}</Badge>
        ) : (
          <Badge variant="outline" className="text-muted-foreground">
            {t('defaultBadge')}
          </Badge>
        )
      }
    >
      <div className="space-y-3">
        {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
        <Textarea
          value={value}
          spellCheck={false}
          className="min-h-32"
          onChange={(e) => setValue(e.target.value)}
        />
        {setting.key === TOKEN_MASKS_KEY && <MaskPreview json={value} />}
        {setting.overridden && setting.updatedBy && (
          <p className="text-xs text-muted-foreground">
            {t('overriddenByLabel', { by: setting.updatedBy })}
            {setting.updatedAt ? ` · ${new Date(setting.updatedAt).toLocaleString()}` : ''}
          </p>
        )}
        <div className="flex gap-2">
          <Button onClick={save} loading={busy} disabled={busy || !dirty}>
            {t('saveOverrideButton')}
          </Button>
          <Button variant="outline" onClick={reset} disabled={busy || !setting.overridden}>
            {t('resetToDefaultButton')}
          </Button>
        </div>
      </div>
    </SectionPanel>
  );
}
