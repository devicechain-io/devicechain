// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Date and time formatting for the replay chrome.
//
// 🔴 EVERY DATE HERE COMES FROM THE RECORDING, NEVER FROM THE WALL CLOCK. The header says
// when the run was recorded, and a header that read the viewer's clock would say "today"
// on a page that replays something from last month. All formatting is pinned to UTC so two
// viewers in different zones read the same label.

import { recordedWallTimeMs, type BoardRecording } from '@devicechain/dashboards';

export function formatRunDate(rec: BoardRecording, locale: string): string {
  return new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeZone: 'UTC' }).format(
    new Date(Date.parse(rec.startedAtUtc)),
  );
}

export function formatRecordedInstant(rec: BoardRecording, locale: string): string {
  return new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'medium', timeZone: 'UTC' }).format(
    new Date(Date.parse(rec.startedAtUtc)),
  );
}

// The recorded UTC wall time at a cursor offset, as HH:MM:SS.
export function formatRecordedClock(rec: BoardRecording, tMs: number): string {
  return new Date(recordedWallTimeMs(rec, tMs)).toISOString().slice(11, 19);
}

// A position as m:ss (the scrub bar's own readout; offsets from the start of the run).
export function formatOffset(tMs: number): string {
  const total = Math.max(0, Math.floor(tMs / 1000));
  const m = Math.floor(total / 60);
  const s = total % 60;
  return `${m}:${String(s).padStart(2, '0')}`;
}
