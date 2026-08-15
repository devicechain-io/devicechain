// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The instance system-settings screen (ADR-042 P2). One tab per setting, each
// with its own editor, over a shared frame that owns everything the editors
// should not have to repeat: the draft, whether it differs from what is stored,
// Save, Reset to default, and the override badge.
//
// It used to be a column of JSON textareas. The reason that was worth replacing
// is not that JSON is ugly — it is that a textarea cannot tell an operator they
// are about to store something broken. Every editor here validates before Save is
// enabled, and the server validates again on write regardless of which editor (or
// which client) produced the value.
//
// 🔴 Drafts live HERE, keyed by setting, not inside each panel. Radix Tabs
// unmounts the inactive panel, so a draft held by the panel would be silently
// discarded by a glance at another tab.
//
// 🔴 A draft is the editor's own FORM STATE, not serialized JSON. Holding JSON and
// re-deriving the form from it each keystroke is the obvious design and it is
// wrong: it forces every editor's serializer to round-trip losslessly through
// half-typed states, which the first three editors all failed to do — a decimal
// point vanished as it was typed, a trailing space could not be entered, and a
// stray character silently dropped its whole field. `toJson` now runs only to
// save and to compare.

import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { PageShell } from '@/components/ui/page-shell';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import { ErrorBanner } from '@/components/ui/error-banner';
import { HintText } from '@/components/ui/hint-text';
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useQuery } from '@/lib/hooks/use-query';
import { listSettings, setSetting, clearSetting, type Setting } from '@/lib/api/settings';
// 🔴 A setting written here can be one another module has memoised — entity.token_masks
// is, by lib/token-masks.ts, for the life of the tab. Without this the operator saves a
// new token mask, opens a create form, and sees the OLD pattern presented as this
// instance's: exactly the confident-wrong-value this screen's own setting exists to
// prevent, self-inflicted. Called for EVERY key rather than matched against the masks
// key, because a gate keyed on a value is a gate that fires only while someone keeps
// the two spellings in step — and the cost of being wrong is one extra query.
import { forgetCachedSettings } from '@/lib/token-masks';
import { useReload, errMessage } from '@/routes/common';
import { SECTIONS } from './sections';
import { RAW_JSON_SECTION } from './RawJsonEditor';
import type { SettingSection } from './registry';

/** Renders a stored JSON value multi-line for editing; an unparseable value is
 *  shown verbatim rather than lost. */
function pretty(json: string): string {
  try {
    return JSON.stringify(JSON.parse(json), null, 2);
  } catch {
    return json;
  }
}

/** Compact form for comparison, so formatting alone never reads as an edit.
 *  Returns the input unchanged when it does not parse — mid-edit raw JSON is
 *  routinely unparseable, and it is still a change worth enabling Save for. */
function compact(json: string): string {
  try {
    return JSON.stringify(JSON.parse(json));
  } catch {
    return json;
  }
}

/**
 * A setting's in-progress edit: which editor is driving it, and that editor's own
 * form state.
 *
 * 🔴 `section` is decided ONCE, when the draft is seeded, and then held. Deciding
 * it per render from the current value would flip the operator between the raw
 * editor and the typed one mid-keystroke as their JSON became parseable — and,
 * because a section carries a component type, would remount the editor and drop
 * focus on every character.
 */
interface Draft {
  section: SettingSection;
  value: unknown;
}

/** Seeds a setting's draft, falling back to raw JSON when its editor cannot model
 *  the whole stored value. */
function seedDraft(setting: Setting, section: SettingSection): Draft {
  const json = pretty(setting.value);
  const seeded = section.seed(json);
  if (seeded !== null) return { section, value: seeded };
  return { section: RAW_JSON_SECTION, value: RAW_JSON_SECTION.seed(json) };
}

export default function SettingsPage() {
  const { t } = useTranslation('adminSettings');
  const [version, reload] = useReload();
  const { data: settings, loading, error } = useQuery(listSettings, [version]);

  // Drafts by setting key. A key with no entry is pristine and re-seeds from the
  // stored value, so a save/reset only has to DELETE its entry.
  const [drafts, setDrafts] = useState<Record<string, Draft>>({});
  const setDraft = (key: string, draft: Draft) => setDrafts((d) => ({ ...d, [key]: draft }));
  const clearDraft = (key: string) =>
    setDrafts((d) => {
      const next = { ...d };
      delete next[key];
      return next;
    });

  const panels = useMemo(
    () =>
      (settings ?? []).map((setting) => ({
        setting,
        // A key the server knows and this build has no editor for still gets a
        // tab — raw, but present. Hiding it would make a configured setting
        // invisible to the operator who configured it.
        section: SECTIONS[setting.key] ?? RAW_JSON_SECTION,
      })),
    [settings],
  );

  if (loading) {
    return (
      <PageShell title={t('title')} description={t('description')}>
        <LoadingState description={t('loadingSettings')} />
      </PageShell>
    );
  }
  if (error) {
    return (
      <PageShell title={t('title')} description={t('description')}>
        <ErrorState description={error} />
      </PageShell>
    );
  }
  if (panels.length === 0) {
    return (
      <PageShell title={t('title')} description={t('description')}>
        <EmptyState description={t('noSettingsDefined')} />
      </PageShell>
    );
  }

  return (
    <PageShell title={t('title')} description={t('description')}>
      <Tabs defaultValue={panels[0].setting.key}>
        <TabsList>
          {panels.map(({ setting, section }) => (
            <TabsTrigger key={setting.key} value={setting.key} className="flex items-center gap-1.5">
              <section.icon size={14} />
              {section.labelKey ? t(section.labelKey) : setting.key}
            </TabsTrigger>
          ))}
        </TabsList>

        {panels.map(({ setting, section }) => (
          <TabsContent key={setting.key} value={setting.key}>
            <SettingPanel
              setting={setting}
              draft={drafts[setting.key] ?? seedDraft(setting, section)}
              onDraftChange={(value) => setDraft(setting.key, { ...(drafts[setting.key] ?? seedDraft(setting, section)), value })}
              onSettled={() => {
                clearDraft(setting.key);
                reload();
              }}
            />
          </TabsContent>
        ))}
      </Tabs>
    </PageShell>
  );
}

function SettingPanel({
  setting,
  draft,
  onDraftChange,
  onSettled,
}: {
  setting: Setting;
  draft: Draft;
  onDraftChange: (value: unknown) => void;
  onSettled: () => void;
}) {
  const { t } = useTranslation('adminSettings');
  const { toast } = useToast();
  const confirm = useConfirm();
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  const { section, value } = draft;
  const json = section.toJson(value);
  const dirty = compact(json) !== compact(setting.value);
  const issue = section.validate(json);
  // The typed editor could not model the whole stored value, so this setting is
  // being edited as raw JSON — say so, rather than leaving the operator to wonder
  // why this tab looks different from the others.
  const unreadable = section === RAW_JSON_SECTION && SECTIONS[setting.key] !== undefined;

  const save = async () => {
    setFormError(null);
    setBusy(true);
    try {
      await setSetting(setting.key, compact(json));
      forgetCachedSettings();
      toast(t('settingSavedToast', { key: setting.key }));
      onSettled();
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
    setFormError(null);
    setBusy(true);
    try {
      await clearSetting(setting.key);
      forgetCachedSettings();
      toast(t('settingResetToast', { key: setting.key }));
      onSettled();
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="max-w-3xl space-y-4">
      <div className="flex items-start justify-between gap-4">
        <div className="space-y-1">
          <p className="font-mono text-xs text-muted-foreground">{setting.key}</p>
          <HintText>{setting.description}</HintText>
        </div>
        {setting.overridden ? (
          <Badge variant="default">{t('overriddenBadge')}</Badge>
        ) : (
          <Badge variant="outline" className="text-muted-foreground">
            {t('defaultBadge')}
          </Badge>
        )}
      </div>

      {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
      {unreadable && <ErrorBanner message={t('unreadableValueWarning')} />}

      <section.Body draft={value} onChange={onDraftChange} />

      {/* The blocking reason is stated even when nothing is dirty yet: an operator
          who opens a tab on a value that is already broken should be told why,
          not left to discover it by editing something unrelated. */}
      {issue && <ErrorBanner message={t(issue.key, issue.values)} />}

      {setting.overridden && setting.updatedBy && (
        <p className="text-xs text-muted-foreground">
          {t('overriddenByLabel', { by: setting.updatedBy })}
          {setting.updatedAt ? ` · ${new Date(setting.updatedAt).toLocaleString()}` : ''}
        </p>
      )}

      <div className="flex gap-2">
        <Button onClick={save} loading={busy} disabled={busy || !dirty || issue !== null}>
          {t('saveOverrideButton')}
        </Button>
        <Button variant="outline" onClick={reset} disabled={busy || !setting.overridden}>
          {t('resetToDefaultButton')}
        </Button>
      </div>
    </div>
  );
}
