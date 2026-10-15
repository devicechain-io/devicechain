// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Shared test harness: the pinned board, a recording that matches it, a fetch stub that
// serves exactly those two files, and a hand-driven clock ticker.

/// <reference types="node" />

import type { ClockTicker } from '@devicechain/dashboards';
import { existsSync, readFileSync } from 'node:fs';
import path from 'node:path';

import { fixtureJson } from '../../../../packages/dashboards/src/testing/recording-fixture';
import { sha256Hex } from '../load';

const FRONTEND = path.resolve(import.meta.dirname, '../../../..');
export const BOARD_PATH = path.join(FRONTEND, 'testdata/sim-dashboards/sp-dashboard.json');
// The converter's real excerpt of the first recorded run. It arrives with the converter
// change and is NOT copied here; suites that need it skip, by name, while it is absent.
export const EXCERPT_PATH = path.join(FRONTEND, 'testdata/board-replay/take2-tyre-excerpt.json');
export const EXCERPT_AVAILABLE = existsSync(EXCERPT_PATH);
export const SHOWCASE_PATH = path.join(FRONTEND, 'testdata/board-replay/sitepulse-showcase.json');

export const ORIGIN = 'https://site.test';
export const RECORDING_URL = `${ORIGIN}/assets/run.json`;
export const BOARD_URL = `${ORIGIN}/assets/sp-dashboard.json`;

export function boardBytes(): Buffer {
  return readFileSync(BOARD_PATH);
}

export function bufferToArrayBuffer(b: Buffer): ArrayBuffer {
  return b.buffer.slice(b.byteOffset, b.byteOffset + b.byteLength) as ArrayBuffer;
}

export async function boardSha256(): Promise<string> {
  return sha256Hex(bufferToArrayBuffer(boardBytes()));
}

// The synthetic fixture recording, pointed at the pinned board by hash.
export async function fixtureRecordingText(mutate?: (doc: Record<string, any>) => void): Promise<string> {
  const doc = fixtureJson();
  doc.board = { ...doc.board, sha256: await boardSha256() };
  mutate?.(doc);
  return JSON.stringify(doc);
}

// The excerpt is a real run's DATA, committed byte-for-byte (its .sha256 sidecar), and its
// `board` block is PROVENANCE: the board at `sourceCommit`, which the converter's own test
// (test_recording_to_board.py) checks against git, not against the working tree, because a
// later board edit is legitimate. The suites that use this helper ask a different question:
// does the recorded data still drive the board as it is NOW (same slot and widget ids, the
// selector, the gauges, the alarm counts)? So the pin is re-pointed at the current fixture,
// in memory, as fixtureRecordingText does for the synthetic recording. The refusal the pin
// exists for (a recording paired with a board it was not pinned to) is proven in
// load.test.ts with a deliberately mismatched hash, and is not weakened by this.
//
// Re-pinning the committed file instead would be wrong twice: it would claim the run was
// recorded against a board it never saw, and its sourceCommit would have to name a branch
// commit that a squash merge leaves unreachable, which the converter test refuses in CI.
export async function excerptOnCurrentBoardText(): Promise<string> {
  const doc = JSON.parse(readFileSync(EXCERPT_PATH, 'utf-8'));
  doc.board = { ...doc.board, sha256: await boardSha256() };
  return JSON.stringify(doc);
}

// The showcase recording (format 2, with positions), re-pinned at the current board for the
// reason excerptOnCurrentBoardText gives.
export async function showcaseOnCurrentBoardText(): Promise<string> {
  const doc = JSON.parse(readFileSync(SHOWCASE_PATH, 'utf-8'));
  doc.board = { ...doc.board, sha256: await boardSha256() };
  return JSON.stringify(doc);
}

export interface FetchStub {
  fetchFn: typeof fetch;
  calls: string[];
  // The options each call was made with.
  inits: Array<RequestInit | undefined>;
}

// A fetch that serves the two named files and records every URL it was asked for. Anything
// else is a 404, and counts as a call.
//
// 🔴 IT ENFORCES THE REQUEST OPTIONS, not just the URL: a call without `credentials: 'omit'`
// and `redirect: 'error'` throws. A stub that ignored its second argument could not tell a
// loader that follows a redirect off the origin from one that refuses to.
export function stubFetch(files: Record<string, string | Buffer>): FetchStub {
  const calls: string[] = [];
  const inits: Array<RequestInit | undefined> = [];
  const fetchFn = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push(url);
    inits.push(init);
    if (init?.redirect !== 'error' || init?.credentials !== 'omit') {
      throw new Error(`fetch(${url}) must pass { redirect: 'error', credentials: 'omit' }, got ${JSON.stringify(init)}`);
    }
    const body = files[url];
    if (body === undefined) return new Response('not found', { status: 404 });
    return new Response(typeof body === 'string' ? body : bufferToArrayBuffer(body), { status: 200 });
  }) as typeof fetch;
  return { fetchFn, calls, inits };
}

// A clock ticker the test drives by hand: frame(ms) delivers one animation frame.
export function manualTicker(): ClockTicker & { frame(ms: number): void; running(): boolean } {
  let handler: ((dt: number) => void) | null = null;
  return {
    start(onFrame) {
      handler = onFrame;
      return () => {
        handler = null;
      };
    },
    frame(ms) {
      handler?.(ms);
    },
    running: () => handler !== null,
  };
}
