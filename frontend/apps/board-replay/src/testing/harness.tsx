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

export function excerptText(): string {
  return readFileSync(EXCERPT_PATH, 'utf-8');
}

export interface FetchStub {
  fetchFn: typeof fetch;
  calls: string[];
}

// A fetch that serves the two named files and records every URL it was asked for. Anything
// else is a 404, and counts as a call.
export function stubFetch(files: Record<string, string | Buffer>): FetchStub {
  const calls: string[] = [];
  const fetchFn = (async (input: RequestInfo | URL) => {
    const url = String(input);
    calls.push(url);
    const body = files[url];
    if (body === undefined) return new Response('not found', { status: 404 });
    return new Response(typeof body === 'string' ? body : bufferToArrayBuffer(body), { status: 200 });
  }) as typeof fetch;
  return { fetchFn, calls };
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
