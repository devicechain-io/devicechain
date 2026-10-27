// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The per-sender connect instructions: what to type into ChirpStack, The Things Stack or a
// generic sender. The strings come from connectSnippet.ts; this component only lays them out and
// decides which sender it is talking about from the integration's registered type.
//
// 🔴 The bearer token is in the Authorization header only while `bearerToken` is passed, which the
// reveal-once dialog does for as long as it is open; the detail page never passes it, so there the
// header holds a placeholder. This component keeps no copy of the token.

import { useTranslation } from 'react-i18next';
import { Input } from '@/components/ui/input';
import { Textarea } from '@/components/ui/textarea';
import { CopyField } from '@/routes/provisioning/SecretReveal';
import type { Integration } from '@/lib/api/integrations';
import { parseGeneric } from './integrationDraft';
import {
  TTS_UPLINK_PATHS,
  chirpstackFields,
  genericCurl,
  ttsFields,
  type SnippetField,
  type SnippetInput,
} from './connectInstructions';

const COMMAND_LABEL = 'snippetCommand';

export function ConnectSnippet({
  integration,
  input,
  idPrefix,
}: {
  integration: Pick<Integration, 'type' | 'generic'>;
  input: SnippetInput;
  /** Keeps element ids unique when two snippets are on screen (the dialog over the page). */
  idPrefix: string;
}) {
  const { t } = useTranslation('integrations');

  if (integration.type === 'CHIRPSTACK_V4' || integration.type === 'TTS_V3') {
    const chirp = integration.type === 'CHIRPSTACK_V4';
    const fields = chirp ? chirpstackFields(input) : ttsFields(input);
    return (
      <div className="space-y-3" data-shot={`integration-snippet-${chirp ? 'chirpstack' : 'tts'}`}>
        <p className="text-sm text-muted-foreground">{t(chirp ? 'snippetChirpstackIntro' : 'snippetTtsIntro')}</p>
        {fields.map((f, i) => (
          <SnippetRow key={f.labelKey} field={f} id={`${idPrefix}-${i}`} />
        ))}
        <ul className="list-disc space-y-1 pl-5 text-sm text-muted-foreground">
          {chirp ? (
            <>
              <li>{t('snippetChirpstackJson')}</li>
              <li>{t('snippetChirpstackCodec')}</li>
              <li>{t('snippetChirpstackQuery')}</li>
            </>
          ) : (
            <>
              <li>{t('snippetTtsPaths', { paths: TTS_UPLINK_PATHS.join(', ') })}</li>
              <li>{t('snippetTtsOthers')}</li>
              <li>{t('snippetTtsCodec')}</li>
            </>
          )}
        </ul>
      </div>
    );
  }

  const g = parseGeneric(integration.generic);
  const curl = genericCurl(input, {
    binary: integration.type === 'GENERIC_BINARY',
    idKind: g.idKind ?? 'header',
    idValue: g.idValue ?? '',
  });
  return (
    <div className="space-y-3" data-shot="integration-snippet-generic">
      <p className="text-sm text-muted-foreground">{t('snippetGenericIntro')}</p>
      <SnippetRow field={{ labelKey: COMMAND_LABEL, value: curl, secret: input.bearerToken != null }} id={`${idPrefix}-curl`} multiline />
      <p className="text-sm text-muted-foreground">{t('snippetGenericRetries')}</p>
    </div>
  );
}

function SnippetRow({ field, id, multiline = false }: { field: SnippetField; id: string; multiline?: boolean }) {
  const { t } = useTranslation('integrations');
  return (
    <div className="space-y-1">
      <label htmlFor={id} className="text-sm font-medium text-foreground">
        {t(field.labelKey)}
      </label>
      <div className="flex items-start gap-2">
        {multiline ? (
          <Textarea
            id={id}
            readOnly
            rows={field.value.split('\n').length}
            value={field.value}
            data-shot-mask={field.secret ? true : undefined}
            className="font-mono text-xs"
          />
        ) : (
          <Input id={id} readOnly value={field.value} className="font-mono text-xs" data-shot-mask={field.secret ? true : undefined} />
        )}
        <CopyField value={field.value} inputId={id} label={t('snippetCopy', { what: t(field.labelKey) })} manualHint={t('copyManually')} />
      </div>
    </div>
  );
}
