// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The provider picker: a named provider and style, plus a key field, in place of
// knowing what a tile template looks like.
//
// 🔴 IT EMITS THE TILE SOURCE AS ONE VALUE. onChange always carries BOTH tileUrl and
// attribution, never one of them, because a tile URL and the credit line its licence
// requires are indivisible — the same rule the server's Merge enforces across tiers.
// A picker that set the URL and left the attribution behind would credit the previous
// provider for the new provider's tiles, which is a licence violation manufactured by
// the UI rather than a cosmetic mismatch.
//
// 🔴 IT HOLDS NO SELECTION STATE. The provider, the style and the key are all DERIVED
// from the tileUrl prop on every render (catalog.ts `recognize`). That is what keeps
// the dropdown honest when someone edits the raw Tile URL field underneath it: with a
// remembered selection, the box would go on naming a provider the URL no longer
// describes, and the next style change would compose from the wrong template.
//
// One component, both tiers — the tenant page and (next) the operator's instance
// default. Two catalogs, or two pickers over one catalog, would drift.

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { FormField } from '@/components/ui/form-field';
import { HintText } from '@/components/ui/hint-text';
import { Input } from '@/components/ui/input';
import { Select } from '@/components/ui/select';
import {
  API_KEY_TOKEN,
  CUSTOM_PROVIDER_ID,
  PROVIDERS,
  composeTileUrl,
  findProvider,
  needsApiKey,
  providerNeedsApiKey,
  recognize,
  type CatalogProvider,
  type CatalogSource,
} from './catalog';

export interface TileSource {
  tileUrl: string;
  attribution: string;
}

interface BasemapPickerProps {
  tileUrl: string;
  attribution: string;
  /** Always both halves. See the note above. */
  onChange: (next: TileSource) => void;
}

export function BasemapPicker({ tileUrl, attribution, onChange }: BasemapPickerProps) {
  const { t } = useTranslation('basemap');

  const match = recognize(tileUrl);

  // The key is derived from the URL when it can be, but a key being TYPED cannot be:
  // until it is complete the composed URL still holds the placeholder, so there is
  // nothing in the URL to read it back from. This holds only that in-progress text,
  // and `match` wins whenever it has an answer — so the field never disagrees with
  // the URL it produced.
  const [typedKey, setTypedKey] = useState('');
  const apiKey = match?.apiKey ? match.apiKey : typedKey;

  const providerId = match?.provider.id ?? CUSTOM_PROVIDER_ID;
  const provider = match?.provider;
  const source = match?.source;

  const emit = (nextSource: CatalogSource, key: string) => {
    onChange({
      tileUrl: composeTileUrl(nextSource.tileUrl, key),
      attribution: nextSource.attribution,
    });
  };

  const pickProvider = (id: string) => {
    // 🔴 THE NEGATIVE CONTROL OF THIS WHOLE COMPONENT. Choosing "Custom…" must leave
    // the fields exactly as they are. It means "I am entering this myself" — for a
    // private tile server, or a provider the catalog does not carry — and a picker
    // that blanked or rewrote the fields here would destroy what someone had already
    // typed at the very moment they said they wanted to type it.
    if (id === CUSTOM_PROVIDER_ID) return;
    const next = findProvider(id);
    if (!next || next.sources.length === 0) return;
    setTypedKey(apiKey);
    emit(next.sources[0], apiKey);
  };

  const pickSource = (id: string) => {
    const next = provider?.sources.find((s) => s.id === id);
    if (next) emit(next, apiKey);
  };

  const setKey = (key: string) => {
    setTypedKey(key);
    if (source) emit(source, key);
  };

  const keyed = provider ? providerNeedsApiKey(provider) : false;
  const keyMissing = keyed && !!source && needsApiKey(source) && apiKey.trim() === '';

  // 🔴 The hole the catalog would otherwise leave open. Because the raw fields stay
  // editable, someone can pick a provider — filling in the correct credit line — and
  // then edit that credit line to something else. The dropdown would go on naming the
  // provider, the tiles would go on being theirs, and the map would carry a credit
  // line their licence does not authorise: exactly the "paste OSM's URL, type © Acme"
  // case this feature exists to make hard, now wearing the catalog's endorsement.
  //
  // A WARNING rather than a block, deliberately. Editing is sometimes right — a
  // composited basemap credits more than one source, and some providers permit a
  // longer form. What is never right is doing it without noticing.
  const attributionDrifted = !!source && attribution.trim() !== source.attribution;

  return (
    <div className="space-y-4">
      <FormField label={t('provider')} htmlFor="bm-provider" description={t('providerHelp')}>
        <Select id="bm-provider" value={providerId} onChange={pickProvider}>
          <option value={CUSTOM_PROVIDER_ID}>{t('providerCustom')}</option>
          {PROVIDERS.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
            </option>
          ))}
        </Select>
      </FormField>

      {provider && provider.sources.length > 1 && (
        <FormField label={t('style')} htmlFor="bm-style">
          <Select id="bm-style" value={source?.id ?? ''} onChange={pickSource}>
            {provider.sources.map((s) => (
              <option key={s.id} value={s.id}>
                {s.name}
              </option>
            ))}
          </Select>
        </FormField>
      )}

      {keyed && (
        <FormField label={t('apiKey')} htmlFor="bm-api-key" description={t('apiKeyHelp')}>
          <Input
            id="bm-api-key"
            value={apiKey}
            onChange={(ev) => setKey(ev.target.value)}
            autoComplete="off"
            spellCheck={false}
            placeholder={t('apiKeyPlaceholder')}
          />
        </FormField>
      )}

      {provider && (
        <ProviderNotes
          provider={provider}
          keyMissing={keyMissing}
          attributionDrifted={attributionDrifted}
        />
      )}
    </div>
  );
}

// The links a chosen provider comes with.
//
// 🔴 We link the provider's own terms and NEVER paraphrase them. "Free", "no key
// needed" and every tier description are claims that go stale silently on someone
// else's website, and we would not notice — the same way an absence claim expires
// without anything going red. A link is always current; a summary is a snapshot we
// have promised to maintain and will not.
function ProviderNotes({
  provider,
  keyMissing,
  attributionDrifted,
}: {
  provider: CatalogProvider;
  keyMissing: boolean;
  attributionDrifted: boolean;
}) {
  const { t } = useTranslation('basemap');
  return (
    <div className="space-y-2">
      {keyMissing && (
        <p className="text-destructive text-xs" data-testid="basemap-key-missing">
          {t('errApiKeyRequired', { token: API_KEY_TOKEN })}
        </p>
      )}
      {attributionDrifted && (
        <p className="text-xs text-amber-600 dark:text-amber-500" data-testid="basemap-attribution-drift">
          {t('warnAttributionEdited', { provider: provider.name })}
        </p>
      )}
      <HintText size="md">
        <a
          className="underline underline-offset-2"
          href={provider.termsUrl}
          target="_blank"
          rel="noreferrer noopener"
        >
          {t('providerTerms', { provider: provider.name })}
        </a>
        {provider.keyUrl && (
          <>
            {' · '}
            <a
              className="underline underline-offset-2"
              href={provider.keyUrl}
              target="_blank"
              rel="noreferrer noopener"
            >
              {t('providerKeyLink')}
            </a>
          </>
        )}
      </HintText>
    </div>
  );
}
