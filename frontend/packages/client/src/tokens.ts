// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Token masks (ADR-042 P3): the client-side template language that mints, and
// soft-validates, entity tokens. A mask is literal text plus typed placeholders,
// and one mask drives three operations:
//
//   generate  fill placeholders to produce a concrete token
//   normalize kebab-case a human string into a {slug}
//   validate  does a token conform to the mask's shape (soft, UI-only)
//
// Masks are advisory: the backend enforces only the security grammar
// (isValidToken below mirrors rdb.ValidateToken). Style — lowercase-kebab,
// prefixes — lives in the mask, applied here in the console, never on the wire.

// The human-readable alphabet for generated ids: lowercase letters + digits minus
// the ambiguous 0 o 1 l i, so a minted token can be read back off a label without
// transcription errors.
const READABLE_ALPHABET = 'abcdefghjkmnpqrstuvwxyz23456789';
const DIGITS = '0123456789';

// The security grammar the backend enforces (rdb.ValidateToken): letters (either
// case), digits, hyphen and underscore, starting alphanumeric. Kept in sync by
// hand — the same regex, mirrored for a pre-submit check so the user sees a bad
// token before the server rejects it.
const TOKEN_GRAMMAR = /^[A-Za-z0-9][A-Za-z0-9_-]*$/;

/** The maximum token length (mirrors rdb.MaxTokenLen). */
export const MAX_TOKEN_LEN = 128;

/** isValidToken mirrors the backend security grammar for a pre-submit check. */
export function isValidToken(token: string): boolean {
  return token.length > 0 && token.length <= MAX_TOKEN_LEN && TOKEN_GRAMMAR.test(token);
}

/** A parsed mask segment: a literal run, or a typed placeholder. */
export type MaskSegment =
  | { kind: 'literal'; text: string }
  | { kind: 'placeholder'; type: 'alphanumeric' | 'numeric' | 'slug' | 'uuid' | 'unknown'; n?: number; raw: string };

const PLACEHOLDER = /\{([a-zA-Z]+)(?:-(\d+))?\}/g;
const KNOWN = ['alphanumeric', 'numeric', 'slug', 'uuid'] as const;

/** parseMask splits a mask into its literal and placeholder segments. */
export function parseMask(mask: string): MaskSegment[] {
  const segments: MaskSegment[] = [];
  let last = 0;
  for (const m of mask.matchAll(PLACEHOLDER)) {
    const at = m.index ?? 0;
    if (at > last) segments.push({ kind: 'literal', text: mask.slice(last, at) });
    const name = m[1].toLowerCase();
    const type = (KNOWN as readonly string[]).includes(name) ? (name as 'alphanumeric') : 'unknown';
    segments.push({ kind: 'placeholder', type, n: m[2] ? parseInt(m[2], 10) : undefined, raw: m[0] });
    last = at + m[0].length;
  }
  if (last < mask.length) segments.push({ kind: 'literal', text: mask.slice(last) });
  return segments;
}

export interface GenerateOptions {
  /** A human string (e.g. the entity's name) that fills {slug} placeholders. */
  seed?: string;
  /** Injectable RNG in [0,1) for deterministic tests; defaults to Math.random. */
  random?: () => number;
  /** Injectable uuid for deterministic tests; defaults to crypto.randomUUID. */
  uuid?: () => string;
}

function randomUUID(): string {
  const g = globalThis as { crypto?: { randomUUID?: () => string } };
  if (g.crypto?.randomUUID) return g.crypto.randomUUID();
  // Fallback for environments without Web Crypto (not the console; belt & braces).
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0;
    return (c === 'x' ? r : (r & 0x3) | 0x8).toString(16);
  });
}

/**
 * generateToken fills a mask's placeholders to produce a concrete token:
 * {alphanumeric-N} → N readable chars, {numeric-N} → N digits, {uuid} → a uuid,
 * {slug} → the normalized seed (or a short readable id when no seed is given, so
 * a "regenerate" button always yields something). Literals pass through.
 */
export function generateToken(mask: string, opts: GenerateOptions = {}): string {
  const rand = opts.random ?? Math.random;
  const uuid = opts.uuid ?? randomUUID;
  // 🔴 The width is bounded before it is used. A mask may name any width the
  // regexp matches, and `Array.from({length: 1e20})` THROWS RangeError — which
  // made this function, called during render by the settings editor and by every
  // create form, a way to kill the page from a stored value. No width past
  // MAX_TOKEN_LEN can mint a legal token anyway, so an absurd one contributes
  // nothing rather than exploding. validateMask refuses such a mask outright;
  // this keeps generation safe for a mask stored before it did.
  const pick = (alphabet: string, n: number) =>
    n > MAX_TOKEN_LEN
      ? ''
      : Array.from({ length: n }, () => alphabet[Math.floor(rand() * alphabet.length)]).join('');

  return parseMask(mask)
    .map((seg) => {
      if (seg.kind === 'literal') return seg.text;
      switch (seg.type) {
        case 'alphanumeric':
          return pick(READABLE_ALPHABET, seg.n ?? 8);
        case 'numeric':
          return pick(DIGITS, seg.n ?? 4);
        case 'slug':
          return opts.seed ? normalizeToken(opts.seed) : pick(READABLE_ALPHABET, 6);
        case 'uuid':
          return uuid();
        default:
          return ''; // an unknown placeholder contributes nothing to a generated token
      }
    })
    .join('');
}

/**
 * normalizeToken kebab-cases a human string into a slug: lower-case, spaces and
 * underscores to hyphens, drop anything else, collapse and trim hyphens.
 * "Ops Overview" → "ops-overview".
 */
export function normalizeToken(input: string): string {
  return input
    .toLowerCase()
    .trim()
    .replace(/[\s_]+/g, '-')
    .replace(/[^a-z0-9-]+/g, '')
    .replace(/-+/g, '-')
    .replace(/^-+|-+$/g, '');
}

function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

/**
 * maskToRegExp compiles a mask into a whole-string RegExp for soft, UI-only
 * conformance checking. It is intentionally lenient (accepts either case in
 * alphanumeric/uuid segments) — the backend, not this, is the authority.
 */
export function maskToRegExp(mask: string): RegExp {
  const body = parseMask(mask)
    .map((seg) => {
      if (seg.kind === 'literal') return escapeRegExp(seg.text);
      switch (seg.type) {
        case 'alphanumeric':
          return `[A-Za-z0-9]{${seg.n ?? 8}}`;
        case 'numeric':
          return `[0-9]{${seg.n ?? 4}}`;
        case 'slug':
          return `[a-z0-9]+(?:-[a-z0-9]+)*`;
        case 'uuid':
          return `[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`;
        default:
          return escapeRegExp(seg.raw); // unknown placeholder must match itself literally
      }
    })
    .join('');
  return new RegExp(`^${body}$`);
}

/** conformsToMask reports whether a token matches the mask's shape (soft check). */
export function conformsToMask(mask: string, token: string): boolean {
  return maskToRegExp(mask).test(token);
}

/** The fixed seed sampleToken fills {slug} placeholders from. */
const SAMPLE_SEED = 'Sample Name';

/**
 * sampleToken generates the token a mask would mint, deterministically. Same
 * generator as generateToken — a pinned RNG and uuid rather than a second
 * implementation, so a sample can never disagree with what a create form
 * actually produces.
 */
export function sampleToken(mask: string, seed: string = SAMPLE_SEED): string {
  return generateToken(mask, {
    seed,
    random: () => 0,
    uuid: () => '3f2b8c14-9d7e-4a51-b6c2-0e8d5a1f7b93',
  });
}

/**
 * Why a mask cannot mint usable tokens. Structured rather than a message
 * because this package is consumed by localized apps — the caller maps a reason
 * to its own wording.
 */
export type MaskProblem =
  | { reason: 'empty' }
  | { reason: 'unknownPlaceholder'; placeholder: string }
  | { reason: 'noPlaceholder' }
  | { reason: 'widthTooLarge'; placeholder: string; max: number }
  | { reason: 'invalidToken'; sample: string };

/**
 * validateMask reports why a mask is unusable, or null when it is fine. It
 * mirrors the server (the tokenmask Go package), which is the authority and
 * refuses the same four things — every one of which fails SILENTLY otherwise:
 *
 *   empty              nothing to mint from
 *   unknownPlaceholder contributes nothing, so "dev-{sulg}" mints "dev-"
 *   noPlaceholder      every entity mints the SAME token; the second create collides
 *   invalidToken       the minted sample is not a legal token (a space, a slash…)
 *
 * 🔴 Keep this NO STRICTER than the server. A check the server does not make
 * would refuse a mask the platform accepts, and the operator would have no way
 * to get past it.
 */
export function validateMask(mask: string): MaskProblem | null {
  if (mask === '') return { reason: 'empty' };
  let placeholders = 0;
  for (const seg of parseMask(mask)) {
    if (seg.kind !== 'placeholder') continue;
    if (seg.type === 'unknown') return { reason: 'unknownPlaceholder', placeholder: seg.raw };
    // 🔴 Checked BEFORE anything samples the mask. This function is called during
    // render, per keystroke — a width of 5e7 froze the tab for seconds building a
    // string nobody would ever see, and 1e20 threw RangeError and killed the
    // page. Neither reached the `invalidToken` check below, which is where a
    // reader would expect an over-long token to be caught.
    if (seg.n !== undefined && seg.n > MAX_TOKEN_LEN) {
      return { reason: 'widthTooLarge', placeholder: seg.raw, max: MAX_TOKEN_LEN };
    }
    placeholders++;
  }
  if (placeholders === 0) return { reason: 'noPlaceholder' };
  const sample = sampleToken(mask);
  if (!isValidToken(sample)) return { reason: 'invalidToken', sample };
  return null;
}

/**
 * resolveMask picks the mask for an entity type from a token-masks map, falling
 * back to the "default" entry and finally to a bare {slug}.
 */
export function resolveMask(masks: Record<string, string>, entityType: string): string {
  return masks[entityType] ?? masks.default ?? '{slug}';
}
