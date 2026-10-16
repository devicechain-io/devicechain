// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useTranslation } from 'react-i18next';

// The re-timed label's words are the embed's own catalogs, in the embed's chunk with the rest
// of it, so the first download of a page that is not an embed does not carry them.
import en from './i18n/locales/en/embed.json';
import es from './i18n/locales/es/embed.json';
import zh from './i18n/locales/zh-CN/embed.json';

const EMBED_CATALOGS: Record<string, { embedLabelRetimed: string }> = { en, es, 'zh-CN': zh };

// The embed's label: the fixed phrase that this is a replay and, re-timed, that its times were
// moved to now. The embed draws it in every state, and on the canvas with the board, so no
// scale or state of the embed shows the board without it.
export function EmbedLabel({ retimed }: { retimed: boolean }) {
  const { t, i18n } = useTranslation();
  return (
    <header
      data-testid="replay-label"
      style={{ flex: '0 0 auto', padding: '6px 12px', fontSize: 13, fontWeight: 600, color: 'hsl(var(--muted-foreground))' }}
    >
      {retimed ? (EMBED_CATALOGS[i18n.language] ?? en).embedLabelRetimed : t('replayBadge')}
    </header>
  );
}
