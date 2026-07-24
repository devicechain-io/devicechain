// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState, type ReactNode } from 'react';
import { Check } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { TokenField } from '@/components/ui/token-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { SectionPanel } from '@/components/ui/section-panel';
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs';
import { TierPill, tierSwatch } from '@/components/tiers/TierPill';
import { useQuery } from '@/lib/hooks/use-query';
import {
  createTenantTier,
  updateTenantTier,
  listGovernanceDimensions,
  listTierColorPalette,
  type AdminTenantTierDetail,
} from '@/lib/api/admin';
import { Textarea, errMessage, dimensionLabel, dimensionUnit } from '@/routes/common';
import { parseTierConfig, buildTierConfigPatch } from '@/routes/admin/tiers/tierConfig';

// TierForm creates a tier (tier absent) or edits one (tier present, token fixed).
//
// The settings fields are rendered from the platform's OWN dimension vocabulary
// rather than a list kept here, so this editor offers exactly the keys the server's
// registry accepts (ADR-065 decision 8) — it cannot misspell one, and a fourth
// governance dimension becomes sellable the day it is declared instead of the day
// someone remembers to add it to this file.
//
// LAYOUT. Create is a single flat form. Edit is TABBED: Basic (identity + color) and
// Settings (ceilings), plus an AI-models tab whose content the caller passes in
// (aiModelsPanel) since it lives on a different admin plane. Basic and Settings are two
// VIEWS OF ONE SAVE, not two independent forms: name/description/color are a full replace
// at the API (a settings-only PATCH that omitted them would clear them), so both tabs
// share this component's state and its single `submit`, and each renders the same Save
// button. The AI tab saves itself, per grant, and carries no such button.
export function TierForm({
  tier,
  onDone,
  aiModelsPanel,
}: {
  tier?: AdminTenantTierDetail;
  onDone: (message: string) => void;
  // When provided (edit only), the form renders tabbed and this is the AI-models tab.
  aiModelsPanel?: ReactNode;
}) {
  const { t } = useTranslation('tiers');
  const editing = tier != null;
  const { data: dimensions, error: dimensionsError } = useQuery(listGovernanceDimensions, []);
  // The palette is fetched, not hardcoded, so the picker offers exactly the tokens the
  // server validates on write — the same reason the ceilings come from the dimension
  // query. Until it lands the picker shows only "no color", which is a safe default.
  const { data: palette } = useQuery(listTierColorPalette, []);

  const [token, setToken] = useState(tier?.token ?? '');
  const [name, setName] = useState(tier?.name ?? '');
  const [description, setDescription] = useState(tier?.description ?? '');
  const [color, setColor] = useState(tier?.color ?? '');
  // Settings are held as raw strings keyed by config key, so an empty field stays
  // distinguishable from a zero — "this tier declares no ceiling here" and "this
  // tier's ceiling is zero" are opposite facts, and the second is not writable at
  // all (a zero ceiling admits nothing, so the server rejects it).
  const [settings, setSettings] = useState<Record<string, string>>(() => parseTierConfig(tier?.config));
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const setSetting = (key: string, value: string) =>
    setSettings((prev) => ({ ...prev, [key]: value }));

  // Whether the settings editor actually RENDERED. Load-bearing, not cosmetic: the
  // fields are built from a query, and until it lands there are no fields — so the
  // form cannot claim to know what this tier's settings should be.
  const settingsEditorReady = dimensions != null && dimensions.length > 0;

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      // What to send as config — or UNDEFINED to leave the tier's settings alone.
      // The decision lives in buildTierConfigPatch, which is where its reasoning and
      // its tests are: sending "{}" built from an editor that never rendered would
      // clear the tier and re-price every tenant at it.
      const config = buildTierConfigPatch(dimensions, settings, tier?.config);
      if (editing) {
        await updateTenantTier(tier.token, {
          name: name.trim() || undefined,
          description: description.trim() || undefined,
          config,
          // Color is a full replace, like name: "" is a real value meaning "no pill".
          color,
        });
        onDone(t('tierUpdatedToast', { token: tier.token }));
      } else {
        await createTenantTier({
          token: token.trim(),
          name: name.trim() || undefined,
          description: description.trim() || undefined,
          config,
          color,
        });
        onDone(t('tierCreatedToast', { token: token.trim() }));
      }
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  // The identity fields (Basic tab / top of the create form): token, name, description,
  // color. Held as a fragment so create renders it inline and edit renders it in a tab.
  const identityFields = (
    <>
      <FormField
        label={t('common:colToken')}
        htmlFor="tier-token"
        description={editing ? t('tokenDescription') : undefined}
      >
        {editing ? (
          <Input id="tier-token" value={token} disabled />
        ) : (
          <TokenField
            id="tier-token"
            entityType="tenant tier"
            value={token}
            onChange={setToken}
            seed={name}
            placeholder={t('tokenPlaceholder')}
          />
        )}
      </FormField>
      <FormField label={t('common:colName')} htmlFor="tier-name">
        <Input
          id="tier-name"
          value={name}
          placeholder={t('namePlaceholder')}
          onChange={(e) => setName(e.target.value)}
        />
      </FormField>
      <FormField
        label={t('common:colDescription')}
        htmlFor="tier-description"
        description={t('descriptionFieldHint')}
      >
        <Textarea
          id="tier-description"
          value={description}
          placeholder={t('descriptionPlaceholder')}
          onChange={(e) => setDescription(e.target.value)}
        />
      </FormField>

      <FormField label={t('colorLabel')} description={t('colorDescription')}>
        <div className="flex items-center gap-3">
          <div className="flex flex-wrap gap-1.5">
            {/* "No color" is a first-class choice, not the absence of one — a neutral
                pill, always offered. */}
            <ColorSwatch
              color=""
              selected={color === ''}
              onSelect={() => setColor('')}
              title={t('noColorLabel')}
            />
            {(palette ?? []).map((c) => (
              <ColorSwatch
                key={c}
                color={c}
                selected={color === c}
                onSelect={() => setColor(c)}
                title={c}
              />
            ))}
          </div>
          {/* Live preview: what the pill will actually look like. It carries the token,
              so the preview shows the token, matching how it renders everywhere else. */}
          <TierPill label={token.trim() || t('tierPillFallback')} color={color} />
        </div>
      </FormField>
    </>
  );

  // The ceilings fields (Settings tab / lower half of the create form).
  const ceilingsFields = (
    <div className="space-y-3">
      <div>
        <h3 className="text-sm font-medium">{t('ceilingsHeading')}</h3>
        <p className="text-sm text-muted-foreground">{t('ceilingsDescription')}</p>
      </div>
      {dimensionsError ? (
        // Say plainly that settings are untouched, not just that something failed.
        // An operator who saves a rename here needs to know the ceilings survived —
        // otherwise the safe behavior looks identical to a silent wipe.
        <p className="text-sm text-destructive">
          {t('settingsUnavailable', { error: dimensionsError })}
        </p>
      ) : !settingsEditorReady ? (
        <p className="text-sm text-muted-foreground">{t('loadingSettings')}</p>
      ) : (
        <div className="grid grid-cols-2 gap-2">
          {(dimensions ?? []).map((d) => (
            <FormFieldPair
              key={d.name}
              label={dimensionLabel(t, d.name, d.label)}
              unit={dimensionUnit(t, d.name, d.rateUnit)}
              rateField={d.rateField}
              burstField={d.burstField}
              rate={settings[d.rateField] ?? ''}
              burst={settings[d.burstField] ?? ''}
              onRate={(v) => setSetting(d.rateField, v)}
              onBurst={(v) => setSetting(d.burstField, v)}
            />
          ))}
        </div>
      )}
    </div>
  );

  // One handler, so the button is the same act wherever it is rendered (both edit tabs).
  const saveButton = (
    <div className="flex gap-2">
      <Button onClick={submit} loading={busy} disabled={busy || (!editing && !token.trim())}>
        {editing ? t('common:saveChanges') : t('createTierButton')}
      </Button>
    </div>
  );

  const errorBanner = formError && (
    <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />
  );

  // Create: one flat form. The caller (NewTierPage) supplies the SectionPanel wrapper.
  if (!editing || aiModelsPanel === undefined) {
    return (
      <div className="space-y-4">
        {errorBanner}
        {identityFields}
        {ceilingsFields}
        {saveButton}
      </div>
    );
  }

  // Edit: tabbed. Basic and Settings are two views of one save (see the component doc), so
  // each tab renders the same Save button and both persist the whole tier. Each tab owns
  // its own SectionPanel, so the detail page renders this form WITHOUT wrapping it.
  return (
    <div className="space-y-4">
      {errorBanner}
      <Tabs defaultValue="basic">
        <TabsList>
          <TabsTrigger value="basic">{t('tabBasic')}</TabsTrigger>
          <TabsTrigger value="settings">{t('tabSettings')}</TabsTrigger>
          <TabsTrigger value="ai">{t('tabAiModels')}</TabsTrigger>
        </TabsList>
        <TabsContent value="basic">
          <SectionPanel>
            <div className="space-y-4">
              {identityFields}
              {saveButton}
            </div>
          </SectionPanel>
        </TabsContent>
        <TabsContent value="settings">
          <SectionPanel>
            <div className="space-y-4">
              {ceilingsFields}
              {saveButton}
            </div>
          </SectionPanel>
        </TabsContent>
        <TabsContent value="ai">{aiModelsPanel}</TabsContent>
      </Tabs>
    </div>
  );
}

// ColorSwatch is one clickable palette chip. It reuses tierSwatch so the chip is the
// exact color the pill will be — a picker that previewed a different shade than it stored
// would be its own small lie. The selected chip carries a check.
function ColorSwatch({
  color,
  selected,
  onSelect,
  title,
}: {
  color: string;
  selected: boolean;
  onSelect: () => void;
  title: string;
}) {
  const { t } = useTranslation('tiers');
  return (
    <button
      type="button"
      onClick={onSelect}
      title={title}
      aria-label={color === '' ? t('noColorLabel') : color}
      aria-pressed={selected}
      className={`flex size-6 items-center justify-center rounded-full ring-1 ring-inset transition ${tierSwatch(
        color,
      )} ${selected ? 'outline outline-2 outline-offset-1 outline-foreground' : 'hover:opacity-80'}`}
    >
      {selected && <Check size={12} />}
    </button>
  );
}

// FormFieldPair renders one dimension's rate + burst inputs. The labels come from
// the dimension itself, so a new one reads correctly without being named here.
function FormFieldPair({
  label,
  unit,
  rateField,
  burstField,
  rate,
  burst,
  onRate,
  onBurst,
}: {
  label: string;
  unit: string;
  rateField: string;
  burstField: string;
  rate: string;
  burst: string;
  onRate: (v: string) => void;
  onBurst: (v: string) => void;
}) {
  const { t } = useTranslation('tiers');
  return (
    <>
      <FormField label={t('common:dimensionRateLabel', { label, unit })} htmlFor={`tier-${rateField}`}>
        <Input
          id={`tier-${rateField}`}
          type="number"
          min="0"
          value={rate}
          placeholder={t('platformDefaultPlaceholder')}
          onChange={(e) => onRate(e.target.value)}
        />
      </FormField>
      <FormField label={t('common:dimensionBurstLabel', { label })} htmlFor={`tier-${burstField}`}>
        <Input
          id={`tier-${burstField}`}
          type="number"
          min="0"
          step="1"
          value={burst}
          placeholder={t('platformDefaultPlaceholder')}
          onChange={(e) => onBurst(e.target.value)}
        />
      </FormField>
    </>
  );
}
