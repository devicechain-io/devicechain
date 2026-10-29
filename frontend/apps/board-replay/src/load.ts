// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Fetch and verify the two files a replay is made of: the recording and the board
// definition it was captured against.
//
// 🔴 THE ONLY NETWORK THIS APP DOES IS TWO SAME-ORIGIN GETS, AND THIS IS WHERE THEY ARE.
// Everything else (the data source, the clock, the widgets with `seedHistory` off) is local.
// Both URLs are refused unless they resolve to this page's own origin, so a mount element
// that names a foreign file cannot make the page dial out.
//
// 🔴 THE BOARD IS VERIFIED BEFORE IT IS PARSED. The recording header carries the SHA-256 of
// the board definition it was captured against; the bytes fetched are hashed and compared,
// and a mismatch refuses to render at all. Showing recorded values on a board other than
// the one they were recorded for would put real numbers under the wrong labels.
//
// Failures are returned as a closed set of CODES (never sentences): the lint gate that
// guards user-facing text sees JSX only, so prose built here would escape it. The messages
// live in the i18n catalogs; `detail` is only ever a parser's or the network's own words.

import {
  migrateToSlots,
  parseBoardRecording,
  parseDashboardDefinition,
  type DashboardDefinitionError,
  type AnchorTarget,
  type BoardRecording,
  type DashboardDefinition,
} from '@devicechain/dashboards';
import { hostWidgetRegistry, type WidgetRegistry } from '@devicechain/host-widgets';
import { preloadWidgets } from '@devicechain/widgets';

export type ReplayErrorCode =
  | 'missingUrl'
  | 'crossOrigin'
  | 'fetch'
  | 'recordingInvalid'
  | 'boardInvalid'
  | 'boardTooNew'
  | 'boardMismatch';

export class ReplayLoadError extends Error {
  constructor(
    public readonly code: ReplayErrorCode,
    public readonly detail: string | null = null,
  ) {
    super(`${code}${detail ? `: ${detail}` : ''}`);
    this.name = 'ReplayLoadError';
  }
}

export interface LoadedReplay {
  recording: BoardRecording;
  definition: DashboardDefinition;
  // The widget registry the board was PARSED with; the player renders with this same one, so
  // an extension widget on a recorded board is recognized and drawn rather than shown as a
  // placeholder.
  registry: WidgetRegistry;
  // The anchors the board binds to; every device of the recorded site is a member.
  siteAnchors: AnchorTarget[];
}

export interface LoadOptions {
  recordingUrl: string | null | undefined;
  // The board definition to pair the recording with. When the host names none, it is the file the
  // recording itself names (its `board.path`, by file name) beside the recording, so one bundle
  // replays any demo's run: nothing here knows which demo a recording came from.
  boardUrl?: string | null | undefined;
  fetchFn?: typeof fetch;
  // The page origin the two URLs must resolve to. Defaults to the current page.
  origin?: string;
  // The registry to parse and render the board with. Defaults to the deployment's shared
  // composition (@devicechain/host-widgets).
  registry?: WidgetRegistry;
}

// The upper bound on either file, so a wrong URL pointing at something huge cannot freeze
// the tab. The whole run is a few hundred KB; the board is a few tens.
export const MAX_FILE_BYTES = 16 << 20;

function detailOf(err: unknown): string | null {
  return err instanceof Error ? err.message : null;
}

function sameOriginUrl(raw: string | null | undefined, origin: string): string {
  if (!raw || raw.trim() === '') throw new ReplayLoadError('missingUrl');
  let url: URL;
  try {
    url = new URL(raw, origin);
  } catch {
    throw new ReplayLoadError('crossOrigin');
  }
  if (url.origin !== origin) throw new ReplayLoadError('crossOrigin');
  return url.toString();
}

async function getBytes(fetchFn: typeof fetch, url: string): Promise<ArrayBuffer> {
  let res: Response;
  try {
    // No credentials and no redirects: a redirect could leave the origin the check above
    // just established.
    res = await fetchFn(url, { credentials: 'omit', redirect: 'error' });
  } catch (err) {
    throw new ReplayLoadError('fetch', detailOf(err));
  }
  if (!res.ok) throw new ReplayLoadError('fetch', `HTTP ${res.status}`);
  const bytes = await res.arrayBuffer();
  if (bytes.byteLength > MAX_FILE_BYTES) throw new ReplayLoadError('fetch', 'file too large');
  return bytes;
}

export async function sha256Hex(bytes: ArrayBuffer): Promise<string> {
  const digest = await globalThis.crypto.subtle.digest('SHA-256', bytes);
  return Array.from(new Uint8Array(digest), (b) => b.toString(16).padStart(2, '0')).join('');
}

// The board a recording names, as a URL beside the recording. Only the file NAME is taken from the
// recording's repository path: where the file is served from is the host's business, and a path
// that climbs or names something other than a JSON file is refused rather than followed.
export function boardBesideRecording(recordingUrl: string, recording: BoardRecording): string {
  const name = recording.board.path.split('/').pop() ?? '';
  if (!/^[A-Za-z0-9][A-Za-z0-9_.-]*\.json$/.test(name) || name.includes('..')) {
    throw new ReplayLoadError('recordingInvalid', 'the recording names a board file this player will not fetch');
  }
  return new URL(name, recordingUrl).toString();
}

// anchorsOf lists the anchor bindings the board declares as slot defaults. The recording
// cannot say which customer the devices were assigned to (it holds devices, not
// membership), so the host declares it, and the only honest source is the board itself.
export function anchorsOf(definition: DashboardDefinition): AnchorTarget[] {
  const out: AnchorTarget[] = [];
  for (const slot of Object.values(definition.slots ?? {})) {
    const binding = slot.defaultBinding;
    if (binding && binding.kind === 'anchor') out.push(binding.anchor);
  }
  return out;
}

export async function loadReplay(options: LoadOptions): Promise<LoadedReplay> {
  const origin = options.origin ?? globalThis.location.origin;
  const fetchFn = options.fetchFn ?? globalThis.fetch.bind(globalThis);
  const registry = options.registry ?? hostWidgetRegistry();
  const recordingUrl = sameOriginUrl(options.recordingUrl, origin);
  const explicitBoardUrl = options.boardUrl ? sameOriginUrl(options.boardUrl, origin) : null;

  const recordingBytes = await getBytes(fetchFn, recordingUrl);
  let recording: BoardRecording;
  try {
    recording = parseBoardRecording(JSON.parse(new TextDecoder().decode(recordingBytes)));
  } catch (err) {
    throw new ReplayLoadError('recordingInvalid', detailOf(err));
  }

  const boardUrl = explicitBoardUrl ?? sameOriginUrl(boardBesideRecording(recordingUrl, recording), origin);
  const boardBytes = await getBytes(fetchFn, boardUrl);
  if ((await sha256Hex(boardBytes)) !== recording.board.sha256) {
    throw new ReplayLoadError('boardMismatch');
  }
  let definition: DashboardDefinition;
  try {
    definition = migrateToSlots(
      parseDashboardDefinition(JSON.parse(new TextDecoder().decode(boardBytes)), { catalog: registry.catalog }),
    );
  } catch (err) {
    // A board from a newer release would draw wrong, not fail: say so rather than play it.
    // The refusal's words are a separate chunk (./refusal), fetched only here: the initial load
    // is budgeted to within a few bytes. They are chosen in the page's language now.
    if ((err as DashboardDefinitionError | null)?.code === 'schemaTooNew') {
      throw new ReplayLoadError('boardTooNew', await import('./refusal').then((m) => m.default()));
    }
    throw new ReplayLoadError('boardInvalid', detailOf(err));
  }

  // The code of the board's lazily loaded widgets arrives before the board is shown, so the
  // first frame is the whole board rather than a board with empty boxes still filling in.
  await preloadWidgets(registry, definition.widgets.map((w) => w.type));

  return { recording, definition, registry, siteAnchors: anchorsOf(definition) };
}
