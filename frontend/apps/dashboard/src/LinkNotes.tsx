// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Notes a kiosk must show even with no header: a link part that was set aside, a board the playlist
// skipped. A wall display has nobody looking at a console, so a typo in its address that only
// warned there would never be seen; this says so on the page, small, and can be dismissed.

import type { TFunction } from 'i18next';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { loadErrorMessage } from './i18n/loadError';
import type { RotateIssue } from './params';
import { ROTATE_MAX_BOARDS, ROTATE_MAX_SECONDS, ROTATE_MIN_BOARDS, ROTATE_MIN_SECONDS, ROTATE_DEFAULT_SECONDS } from './params';
import type { Skipped } from './rotation';

const EVERY_KEYS = {
  unparseable: 'view:rotateNoteEveryUnparseable',
  tooShort: 'view:rotateNoteEveryTooShort',
  tooLong: 'view:rotateNoteEveryTooLong',
} as const;

// One issue as a sentence, from the catalog. An exhaustive switch: a new issue is a type error here
// until it has words.
export function rotateIssueNote(issue: RotateIssue, t: TFunction): string {
  switch (issue.kind) {
    case 'token':
      return t('view:rotateNoteToken', { value: issue.value });
    case 'too-few':
      return t(issue.usable === 0 ? 'view:rotateNoteNone' : 'view:rotateNoteOne', { min: ROTATE_MIN_BOARDS });
    case 'too-many':
      return t('view:rotateNoteTooMany', { max: ROTATE_MAX_BOARDS, dropped: issue.dropped });
    case 'every':
      return t(EVERY_KEYS[issue.reason], {
        value: issue.value,
        min: ROTATE_MIN_SECONDS,
        max: ROTATE_MAX_SECONDS,
        seconds: ROTATE_DEFAULT_SECONDS,
      });
    case 'every-alone':
      return t('view:rotateNoteEveryAlone');
    case 'dashboard-ignored':
      return t('view:rotateNoteDashboard');
  }
}

export function skippedNote(skipped: Skipped, t: TFunction): string {
  return t('view:rotateNoteSkipped', { token: skipped.token, reason: loadErrorMessage(skipped.error, t) });
}

export interface Note {
  id: string;
  text: string;
}

// The stack itself: top centre, small, over the board. `data-rotate-notes` is the test and
// screenshot hook.
export function NoteStack({ notes, onDismiss }: { notes: Note[]; onDismiss: (id: string) => void }) {
  const { t } = useTranslation();
  if (notes.length === 0) return null;
  return (
    <div
      data-rotate-notes=""
      role="status"
      style={{
        position: 'fixed',
        top: 8,
        left: '50%',
        transform: 'translateX(-50%)',
        zIndex: 20,
        display: 'flex',
        flexDirection: 'column',
        gap: 4,
        maxWidth: 'min(640px, calc(100% - 32px))',
      }}
    >
      {notes.map((note) => (
        <div
          key={note.id}
          data-rotate-note=""
          style={{
            display: 'flex',
            alignItems: 'flex-start',
            gap: 8,
            padding: '4px 8px',
            fontSize: 12,
            borderRadius: 6,
            border: '1px solid hsl(var(--border))',
            background: 'hsl(var(--card))',
            color: 'hsl(var(--muted-foreground))',
          }}
        >
          <span style={{ flex: '1 1 auto', overflowWrap: 'anywhere' }}>{note.text}</span>
          <button
            type="button"
            aria-label={t('view:rotateDismiss')}
            onClick={() => onDismiss(note.id)}
            style={{ flex: '0 0 auto', border: 0, background: 'transparent', color: 'inherit', cursor: 'pointer', padding: '0 2px' }}
          >
            ×
          </button>
        </div>
      ))}
    </div>
  );
}

// The notes for what a link's `rotate` / `every` set aside, shown on whatever the link opened.
export function LinkNotes({ issues }: { issues: RotateIssue[] | undefined }) {
  const { t } = useTranslation();
  const [dismissed, setDismissed] = useState<ReadonlySet<string>>(new Set());
  const notes = (issues ?? [])
    .map((issue, i) => ({ id: `${i}`, text: rotateIssueNote(issue, t) }))
    .filter((n) => !dismissed.has(n.id));
  return <NoteStack notes={notes} onDismiss={(id) => setDismissed((d) => new Set(d).add(id))} />;
}
