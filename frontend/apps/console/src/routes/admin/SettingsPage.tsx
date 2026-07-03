// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useMemo, useState } from 'react';
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
      .map(([type, mask]) => [type, generateToken(mask as string, { seed: 'Sample Name' })] as const);
  }, [json]);

  if (!entries || entries.length === 0) return null;
  return (
    <div className="rounded-md border border-border bg-muted/30 p-3 text-xs">
      <p className="mb-2 font-medium text-muted-foreground">
        Preview — a sample token per type (from the seed “Sample Name”)
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
  const [version, reload] = useReload();
  const { data: settings, loading, error } = useQuery(listSettings, [version]);

  return (
    <PageShell
      title="System settings"
      description="Instance-wide configuration. Defaults live in code; only your overrides are stored, so a setting always resolves even before it is ever set."
    >
      {loading ? (
        <LoadingState description="Loading settings…" />
      ) : error ? (
        <ErrorState description={error} />
      ) : !settings || settings.length === 0 ? (
        <EmptyState description="No settings are defined." />
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
      setFormError('Value must be valid JSON.');
      return;
    }
    setBusy(true);
    try {
      await setSetting(setting.key, compact);
      toast(`Setting “${setting.key}” saved`);
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
        title: 'Reset to default?',
        description: `“${setting.key}” will revert to its built-in default and your override will be removed.`,
        confirmLabel: 'Reset',
      }))
    )
      return;
    setBusy(true);
    setFormError(null);
    try {
      await clearSetting(setting.key);
      toast(`Setting “${setting.key}” reset to default`);
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
          <Badge variant="default">Overridden</Badge>
        ) : (
          <Badge variant="outline" className="text-muted-foreground">
            Default
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
            Overridden by {setting.updatedBy}
            {setting.updatedAt ? ` · ${new Date(setting.updatedAt).toLocaleString()}` : ''}
          </p>
        )}
        <div className="flex gap-2">
          <Button onClick={save} loading={busy} disabled={busy || !dirty}>
            Save override
          </Button>
          <Button variant="outline" onClick={reset} disabled={busy || !setting.overridden}>
            Reset to default
          </Button>
        </div>
      </div>
    </SectionPanel>
  );
}
