// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { useQuery } from '@/lib/hooks/use-query';
import { errMessage } from '@/routes/common';
import { listDeviceTypes, createDevices } from '@/lib/api/device-management';

// Mirrors model.MaxBulkDeviceCount — the server rejects anything larger, but the
// form gives the same ceiling as inline feedback rather than a round-trip error.
const MAX_COUNT = 1000;

// The token grammar (core.ValidateToken): letters, digits, hyphen, underscore,
// starting with a letter or digit. Used for the preview's client-side check only;
// the server re-validates every rendered token authoritatively.
const TOKEN_GRAMMAR = /^[A-Za-z0-9][A-Za-z0-9_-]*$/;
const INDEX_PLACEHOLDER = /\{n(?::0(\d+)d)?\}/;

// MAX_PAD_WIDTH mirrors core.MaxTemplatePadWidth — the server rejects anything
// wider, and the preview must clamp so a huge typed width can't allocate a giant
// string (or throw RangeError) during render.
const MAX_PAD_WIDTH = 128;

// renderPreview renders a template the way the server does for a single index —
// {n} / {n:0Wd} from the index — but shows {random} as a visible marker, since the
// server fills it with fresh randomness the client cannot predict. The pad width is
// clamped so an absurd typed width never freezes the tab. Preview only.
function renderPreview(template: string, index: number): string {
  return template
    .replace(/\{n(?::0(\d+)d)?\}/g, (_m, width?: string) =>
      width ? String(index).padStart(Math.min(Number(width), MAX_PAD_WIDTH), '0') : String(index),
    )
    .replace(/\{random\}/g, '‹random›');
}

// DeviceBulkForm creates a whole fleet of devices from templates in one call
// (createDevices). The token/name templates understand {n} and {n:0Wd} (the
// 1-based index, optionally zero-padded); the external-id template also understands
// {random} for a per-device random business id.
export function DeviceBulkForm({ onDone }: { onDone: (message: string) => void }) {
  const { t } = useTranslation('devices');
  const { data: types } = useQuery(
    () => listDeviceTypes({ pageNumber: 1, pageSize: 1000 }).then((r) => r.results),
    [],
  );
  const typeOptions: ComboboxOption[] = (types ?? []).map((t) => ({
    value: t.token,
    label: t.name || t.token,
  }));

  const [deviceTypeToken, setDeviceTypeToken] = useState('');
  const [count, setCount] = useState(10);
  const [startIndex, setStartIndex] = useState(1);
  const [tokenTemplate, setTokenTemplate] = useState('device-{n:04d}');
  const [nameTemplate, setNameTemplate] = useState('Device {n}');
  const [externalIdTemplate, setExternalIdTemplate] = useState('');
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // Client-side validity, mirroring the server's expandBulkDeviceRequest, so the
  // submit button and inline hints react before a round-trip.
  const validation = useMemo(() => {
    if (!deviceTypeToken) return t('chooseDeviceType');
    if (!Number.isInteger(count) || count < 1) return t('countMustBeAtLeastOne');
    if (count > MAX_COUNT) return t('countExceedsMax', { max: MAX_COUNT });
    if (!Number.isInteger(startIndex) || startIndex < 1) return t('startIndexMustBePositive');
    if (!tokenTemplate.trim()) return t('tokenTemplateRequired');
    if (tokenTemplate.includes('{random}')) return t('tokenTemplateNoRandom');
    if (!INDEX_PLACEHOLDER.test(tokenTemplate)) return t('tokenTemplateNeedsIndex');
    if (nameTemplate.includes('{random}')) return t('nameTemplateNoRandom');
    const firstToken = renderPreview(tokenTemplate, startIndex);
    if (!TOKEN_GRAMMAR.test(firstToken)) return t('renderedTokenInvalid', { token: firstToken });
    // A fixed external id would collide across the batch (external ids are unique
    // per tenant), so require it to vary when creating more than one device.
    const ext = externalIdTemplate.trim();
    if (ext && count > 1 && !INDEX_PLACEHOLDER.test(ext) && !ext.includes('{random}'))
      return t('externalIdTemplateNeedsVariation');
    return null;
  }, [deviceTypeToken, count, startIndex, tokenTemplate, nameTemplate, externalIdTemplate, t]);

  // A few sample rows (first three, an ellipsis, then the last) so the operator
  // sees exactly what the templates will produce before committing.
  const preview = useMemo(() => {
    if (count < 1 || !tokenTemplate) return [];
    const end = startIndex + count - 1;
    const indices =
      count <= 4 ? range(startIndex, end) : [startIndex, startIndex + 1, startIndex + 2, null, end];
    return indices.map((i) =>
      i === null
        ? null
        : {
            index: i,
            token: renderPreview(tokenTemplate, i),
            name: nameTemplate ? renderPreview(nameTemplate, i) : '',
            externalId: externalIdTemplate ? renderPreview(externalIdTemplate, i) : '',
          },
    );
  }, [count, startIndex, tokenTemplate, nameTemplate, externalIdTemplate]);

  const submit = async () => {
    if (validation) {
      setFormError(validation);
      return;
    }
    setFormError(null);
    setBusy(true);
    try {
      const created = await createDevices({
        deviceTypeToken,
        count,
        startIndex,
        tokenTemplate: tokenTemplate.trim(),
        nameTemplate: nameTemplate.trim() || undefined,
        externalIdTemplate: externalIdTemplate.trim() || undefined,
      });
      onDone(t('createdDevicesToast', { count: created.length }));
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-4">
      {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}

      <FormField label={t('deviceTypeLabel')} htmlFor="b-type" description={t('deviceTypeBatchHint')}>
        <Combobox
          options={typeOptions}
          value={deviceTypeToken}
          onChange={setDeviceTypeToken}
          placeholder={t('selectDeviceTypePlaceholder')}
        />
      </FormField>

      <div className="grid grid-cols-2 gap-3">
        <FormField label={t('countLabel')} htmlFor="b-count" description={`1 – ${MAX_COUNT}`}>
          <Input
            id="b-count"
            type="number"
            min={1}
            max={MAX_COUNT}
            value={count}
            onChange={(e) => setCount(Math.floor(Number(e.target.value)))}
          />
        </FormField>
        <FormField label={t('startIndexLabel')} htmlFor="b-start" description={t('startIndexHint')}>
          <Input
            id="b-start"
            type="number"
            min={1}
            value={startIndex}
            onChange={(e) => setStartIndex(Math.floor(Number(e.target.value)))}
          />
        </FormField>
      </div>

      <FormField
        label={t('tokenTemplateLabel')}
        htmlFor="b-token"
        description={t('tokenTemplateHint')}
      >
        <Input
          id="b-token"
          value={tokenTemplate}
          onChange={(e) => setTokenTemplate(e.target.value)}
          placeholder={t('tokenTemplatePlaceholder')}
          className="font-mono"
        />
      </FormField>

      <FormField label={t('nameTemplateLabel')} htmlFor="b-name" description={t('nameTemplateHint')}>
        <Input
          id="b-name"
          value={nameTemplate}
          onChange={(e) => setNameTemplate(e.target.value)}
          placeholder={t('nameTemplatePlaceholder')}
        />
      </FormField>

      <FormField
        label={t('externalIdTemplateLabel')}
        htmlFor="b-ext"
        description={t('externalIdTemplateHint')}
      >
        <Input
          id="b-ext"
          value={externalIdTemplate}
          onChange={(e) => setExternalIdTemplate(e.target.value)}
          placeholder={t('externalIdTemplatePlaceholder')}
          className="font-mono"
        />
      </FormField>

      {preview.length > 0 && (
        <div className="rounded-md border border-border bg-muted/30 p-3">
          <div className="mb-2 text-xs font-medium uppercase tracking-wide text-muted-foreground">
            {t('previewLabel')}
          </div>
          <div className="space-y-1 font-mono text-xs">
            {preview.map((row, i) =>
              row === null ? (
                <div key={`gap-${i}`} className="text-muted-foreground">
                  …
                </div>
              ) : (
                <div key={row.index} className="flex flex-wrap gap-x-3 text-foreground">
                  <span>{row.token}</span>
                  {row.name && <span className="text-muted-foreground">{row.name}</span>}
                  {row.externalId && <span className="text-muted-foreground">{row.externalId}</span>}
                </div>
              ),
            )}
          </div>
        </div>
      )}

      <div className="flex items-center gap-3">
        <Button onClick={submit} loading={busy} disabled={busy || validation !== null}>
          {t('createDeviceButton', { count: count > 0 ? count : 0 })}
        </Button>
        {validation && !formError && <span className="text-sm text-muted-foreground">{validation}</span>}
      </div>
    </div>
  );
}

// range returns the inclusive integer range [from, to].
function range(from: number, to: number): number[] {
  const out: number[] = [];
  for (let i = from; i <= to; i++) out.push(i);
  return out;
}
