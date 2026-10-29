// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The one surface behind "Import", "Clone" and "Start from a template".
//
// Every door hands this drawer a JSON text (or none, for a paste/upload) and it does the
// same things in the same order: read it through the editor's own parser and widget
// registry, show what is wrong or foreign, take a NEW token, and create the dashboard as a
// DRAFT. It never overwrites -- the create call is the only write, and the token field
// refuses a token that is taken -- and it never publishes; publishing stays an explicit act
// in the editor, where a widget this console cannot resolve still blocks it.

import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Upload } from 'lucide-react';
import { FormDrawer } from '@/components/registry';
import { Button } from '@/components/ui/button';
import { ErrorBanner } from '@/components/ui/error-banner';
import { FilePicker } from '@/components/ui/file-picker';
import { FormField } from '@/components/ui/form-field';
import { Input } from '@/components/ui/input';
import { TokenField } from '@/components/ui/token-field';
import { Textarea } from '@/components/ui/textarea';
import { createDashboard, getDashboard } from '@/lib/api/dashboards';
import { errMessage } from '@/routes/common';
import { EntityPicker } from '../editor/EntityPicker';
import { defaultWidgetRegistry } from '../editor/widgetRegistry';
import { entityExists } from './entityExists';
import {
  applyBindingChoices,
  buildImportPayload,
  byteLength,
  checkBindingRefs,
  collectBindingRefs,
  DashboardImportError,
  MAX_DEFINITION_BYTES,
  MAX_IMPORT_TEXT_BYTES,
  prepareImport,
  type BindingChoices,
  type BindingRefStatus,
  type PreparedImport,
} from './importDefinition';
import '@/i18n/lazy/dashboards';

// Where the JSON came from. `text` is absent for the paste/upload door.
export interface ImportSource {
  text?: string;
  // Suggested name for the new dashboard (a clone offers "Copy of ...").
  name?: string;
  description?: string;
}

export function DashboardImportDrawer({
  open,
  onOpenChange,
  title,
  source,
  onCreated,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  source: ImportSource;
  onCreated: (token: string) => void;
}) {
  const { t } = useTranslation('dashboards');
  return (
    <FormDrawer shot="dashboard-import" open={open} onOpenChange={onOpenChange} title={title} description={t('importDescription')}>
      {/* Mounted only while open: closing the drawer drops the half-reviewed import. */}
      {open && <ImportFlow source={source} onCreated={onCreated} />}
    </FormDrawer>
  );
}

interface Review {
  prepared: PreparedImport;
  statuses: BindingRefStatus[];
}

function ImportFlow({ source, onCreated }: { source: ImportSource; onCreated: (token: string) => void }) {
  const { t } = useTranslation('dashboards');
  const [text, setText] = useState(source.text ?? '');
  const [review, setReview] = useState<Review | null>(null);
  const [choices, setChoices] = useState<BindingChoices>({});
  const [token, setToken] = useState('');
  const [name, setName] = useState(source.name ?? '');
  const [description, setDescription] = useState(source.description ?? '');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // The text an import reads is bounded separately from the definition it parses to.
  const textTooLarge = () => t('importErrorTextTooLarge', { limit: MAX_IMPORT_TEXT_BYTES / (1 << 20) });

  const describe = (err: unknown): string => {
    if (err instanceof DashboardImportError) {
      switch (err.code) {
        case 'empty':
          return t('importErrorEmpty');
        case 'tooLarge':
          return err.limitBytes === MAX_IMPORT_TEXT_BYTES
            ? textTooLarge()
            : t('importErrorTooLarge', { limit: MAX_DEFINITION_BYTES / (1 << 20) });
        case 'notJson':
          return t('importErrorNotJson', { detail: err.message });
        case 'tooNew':
          return t('schemaTooNew');
        case 'invalid':
          return t('importErrorInvalid', { detail: err.message });
      }
    }
    return errMessage(err);
  };

  const runReview = async (input: string) => {
    setError(null);
    setBusy(true);
    try {
      const prepared = prepareImport(input, defaultWidgetRegistry().catalog);
      const statuses = await checkBindingRefs(collectBindingRefs(prepared.definition), entityExists);
      setChoices({});
      setName((current) => current || prepared.definition.title);
      setReview({ prepared, statuses });
    } catch (err) {
      setReview(null);
      setError(describe(err));
    } finally {
      setBusy(false);
    }
  };

  // A template or a clone arrives with its text: review it straight away.
  useEffect(() => {
    if (source.text) void runReview(source.text);
    // Runs once per mount; the drawer remounts the flow for every open.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const pickFile = (file: File) => {
    // Refuse by size BEFORE reading the file into the page.
    if (file.size > MAX_IMPORT_TEXT_BYTES) {
      setReview(null);
      setError(textTooLarge());
      return;
    }
    file
      .text()
      .then((content) => {
        setText(content);
        return runReview(content);
      })
      .catch((err: unknown) => setError(errMessage(err)));
  };

  const create = async () => {
    if (!review) return;
    setError(null);
    setBusy(true);
    try {
      const repaired = applyBindingChoices(review.prepared.definition, review.statuses, choices);
      const definition = buildImportPayload(repaired, name);
      const created = await createDashboard({
        token: token.trim(),
        name: name.trim() || undefined,
        description: description.trim() || undefined,
        definition,
      });
      onCreated(created.token);
    } catch (err) {
      setError(describe(err));
    } finally {
      setBusy(false);
    }
  };

  const oversize = byteLength(text) > MAX_IMPORT_TEXT_BYTES;

  return (
    <div className="space-y-4">
      {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}

      {!review && !source.text && (
        <>
          <FormField label={t('importJsonLabel')} htmlFor="import-json" description={t('importJsonHint')}>
            <Textarea
              id="import-json"
              value={text}
              rows={10}
              className="font-mono text-xs"
              onChange={(e) => setText(e.target.value)}
            />
          </FormField>
          <div className="flex gap-2">
            <FilePicker accept="application/json,.json" onPick={pickFile}>
              <Upload size={14} /> {t('importChooseFile')}
            </FilePicker>
            <Button onClick={() => void runReview(text)} loading={busy} disabled={busy || !text.trim() || oversize}>
              {t('importReview')}
            </Button>
          </div>
          {oversize && <p className="text-sm text-destructive">{textTooLarge()}</p>}
        </>
      )}

      {!review && source.text && busy && <p className="text-sm text-muted-foreground">{t('importChecking')}</p>}
      {!review && source.text && !busy && error && (
        <Button variant="outline" onClick={() => void runReview(source.text ?? '')}>
          {t('importRetry')}
        </Button>
      )}

      {review && (
        <>
          <FormField label={t('common:colToken')} htmlFor="import-token" description={t('importTokenHint')}>
            <TokenField
              id="import-token"
              entityType="dashboard"
              value={token}
              onChange={setToken}
              seed={name}
              placeholder={t('createTokenPlaceholder')}
              checkAvailability={(candidate) => getDashboard(candidate).then((d) => d === null)}
            />
          </FormField>
          <FormField label={t('common:colName')} htmlFor="import-name">
            <Input id="import-name" value={name} onChange={(e) => setName(e.target.value)} />
          </FormField>
          <FormField label={t('common:colDescription')} htmlFor="import-description">
            <Textarea id="import-description" value={description} onChange={(e) => setDescription(e.target.value)} />
          </FormField>

          <ReviewNotes prepared={review.prepared} />
          <BindingRows statuses={review.statuses} choices={choices} onChoose={(slot, v) => setChoices((c) => ({ ...c, [slot]: v }))} />

          <p className="text-sm text-muted-foreground">{t('importDraftNote')}</p>
          <div className="flex gap-2">
            <Button onClick={() => void create()} loading={busy} disabled={busy || !token.trim()}>
              {t('importCreate')}
            </Button>
          </div>
        </>
      )}
    </div>
  );
}

function ReviewNotes({ prepared }: { prepared: PreparedImport }) {
  const { t } = useTranslation('dashboards');
  const { unresolved, optionIssues, concreteSelectors, externalOrigins } = prepared;
  if (
    unresolved.length === 0 &&
    optionIssues.length === 0 &&
    concreteSelectors === 0 &&
    externalOrigins.length === 0
  ) {
    return null;
  }
  return (
    <div className="space-y-2 rounded-md border border-border p-3 text-sm" role="status">
      {unresolved.length > 0 && (
        <div>
          <p className="font-medium">{t('importUnresolvedHeading', { n: unresolved.length })}</p>
          <p className="text-muted-foreground">{t('importUnresolvedNote')}</p>
          <ul className="list-disc pl-5 text-muted-foreground">
            {unresolved.map((u) => (
              <li key={u.widgetId}>
                {u.type || u.widgetId}: {u.reason.message}
              </li>
            ))}
          </ul>
        </div>
      )}
      {optionIssues.length > 0 && (
        <div>
          <p className="font-medium">{t('importOptionIssuesHeading', { n: optionIssues.length })}</p>
          <ul className="list-disc pl-5 text-muted-foreground">
            {optionIssues.map((i) => (
              <li key={`${i.widgetId}:${i.key}:${i.code}`}>
                {i.widgetId}: {i.message}
              </li>
            ))}
          </ul>
        </div>
      )}
      {concreteSelectors > 0 && <p className="text-muted-foreground">{t('importConcreteNote', { n: concreteSelectors })}</p>}
      {externalOrigins.length > 0 && (
        <div>
          <p className="font-medium">{t('importExternalHeading', { n: externalOrigins.length })}</p>
          <p className="text-muted-foreground">{t('importExternalNote')}</p>
          <ul className="list-disc pl-5 font-mono text-xs text-muted-foreground">
            {externalOrigins.map((origin) => (
              <li key={origin}>{origin}</li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}

const KIND_KEYS = {
  device: 'importKindDevice',
  customer: 'importKindCustomer',
  area: 'importKindArea',
  asset: 'importKindAsset',
} as const;

function BindingRows({
  statuses,
  choices,
  onChoose,
}: {
  statuses: BindingRefStatus[];
  choices: BindingChoices;
  onChoose: (slot: string, token: string) => void;
}) {
  const { t } = useTranslation('dashboards');
  // Bindings that resolve here need nothing from the user and are not listed.
  const needs = statuses.filter((s) => !s.exists || s.token === null);
  if (needs.length === 0) return null;
  return (
    <fieldset className="space-y-3 rounded-md border border-border p-3">
      <legend className="px-1 text-sm font-medium">{t('importBindingsHeading')}</legend>
      <p className="text-sm text-muted-foreground">{t('importBindingsNote')}</p>
      {needs.map((s) => (
        <FormField
          key={s.slot}
          label={s.label}
          htmlFor={`import-bind-${s.slot}`}
          description={
            s.token === null
              ? t('importBindingUnbound')
              : t('importBindingMissing', { kind: t(KIND_KEYS[s.kind]), token: s.token })
          }
        >
          {s.supported ? (
            <EntityPicker
              id={`import-bind-${s.slot}`}
              kind={s.kind}
              value={choices[s.slot] ?? ''}
              onChange={(v) => onChoose(s.slot, v)}
              placeholder={t('importBindingLeaveUnbound')}
            />
          ) : (
            <p className="text-sm text-muted-foreground">{t('importBindingUnsupported')}</p>
          )}
        </FormField>
      ))}
    </fieldset>
  );
}
