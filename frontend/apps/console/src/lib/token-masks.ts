// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Reading the operator's entity token masks over whichever session the asking
// screen actually holds.
//
// 🔴 WHY THIS MODULE EXISTS. The masks are one instance-global setting, and for a
// long time the console read them over one lane: the identity lane, because that is
// where the settings schema lives. But the identity token expires in fifteen minutes
// and has no refresh path, while a tenant session lasts up to seven days. So a
// tenant user who had been working for a quarter of an hour opened a create form,
// the masks query 401'd, the failure was swallowed, and every token minted from then
// on used the built-in `{slug}` pattern instead of the operator's. Nothing was shown
// and nothing was logged. The value looked plausible, which is the only reason it
// survived — a blank field would have been reported the first day.
//
// The lane, not the gate, was the defect: the resolver was deliberately permissive
// and said so. It was reachable only by callers whose token was about to die.
//
// 🔑 What actually fixes it is that a TENANT lane now exists at all, plus the rule
// that a lane is only attempted when its session is live. Tenant-first ordering is a
// preference on top of that — ask the long-lived session first — not the mechanism.
//
// Being precise about that matters, because it also fixes a second bug and it would
// be easy to credit the wrong half. Asking over a dead identity token calls
// expireIdentity(), which raises the console's global "your session expired" flag and
// hides the admin menu entry — so merely opening a create form on a perfectly healthy
// tenant session used to tell the user their session had ended. The old code called
// the identity lane unconditionally. What stops it now is the `isIdentityAuthenticated`
// GUARD below, which is already false once the token has expired
// (AuthProvider: `identityAlive = identity !== null && !isExpired(...)`). Reorder the
// two lanes and that bug stays fixed; drop the guard and it comes back.
//
// Modelled on lib/capabilities.tsx, which solves the same both-lanes problem and
// establishes the rule this follows: a 401 on one lane means "try the other", and
// every lane failing means SAY SO rather than substituting a plausible answer.
import { getTenantTokenMasks } from '@/lib/api/user-management';
import { getIdentityTokenMasks } from '@/lib/api/settings';
import { parseJsonObject } from '@/lib/utils';

/** Which sessions are live, so a lane is only tried when it can succeed. */
export interface MaskLanes {
  isAuthenticated: boolean;
  isIdentityAuthenticated: boolean;
}

/**
 * The masks, or `null` when they could not be read.
 *
 * 🔴 `null` and `{}` are DIFFERENT and the distinction is the whole point. `{}` means
 * the operator configured no masks, so the built-in default is the truth. `null` means
 * nobody knows, so the built-in default is a guess. Collapsing them is what made this
 * invisible; a caller that renders them identically has reintroduced the bug.
 */
export type TokenMasks = Record<string, string> | null;

// Cached across fields once read. Only a SUCCESS is cached: a failure must not
// poison the rest of the session, since the usual cause is one lane being
// momentarily unavailable. Note the inverse hazard, which is real — a successful
// `{}` is cached for the life of the tab, so a mask configured after the console
// loaded is not picked up until a reload. That is the pre-existing behaviour and is
// acceptable for a value that changes about never; it is recorded here so the next
// reader does not mistake it for an oversight.
let cache: Record<string, string> | null = null;
// Warn once per session rather than once per create form: a repeated warning while a
// lane is down is noise that trains people to ignore it. Never reset on success — once
// `cache` is set, `getTokenMasks` returns early and the warn block is unreachable.
let warned = false;

function parse(raw: string): Record<string, string> {
  return parseJsonObject(raw) as Record<string, string>;
}

export async function getTokenMasks(lanes: MaskLanes): Promise<TokenMasks> {
  if (cache) return cache;

  // Tenant first: it is the long-lived session, and on a tenant-scoped screen it is
  // the only one that reliably exists. The admin screens fall through to the identity
  // lane, which is the only one THEY are guaranteed — an operator creating the
  // instance's first tenant holds no tenant session at all.
  const attempts: Array<() => Promise<string>> = [];
  if (lanes.isAuthenticated) attempts.push(getTenantTokenMasks);
  if (lanes.isIdentityAuthenticated) attempts.push(getIdentityTokenMasks);

  for (const attempt of attempts) {
    try {
      const masks = parse(await attempt());
      cache = masks;
      return masks;
    } catch {
      // Try the next lane before giving up: one lane refusing says nothing about the
      // other, and treating it as final is exactly the mistake being fixed.
    }
  }

  // Every available lane failed — or there were none, which is the signed-out case.
  // Answer "unknown" and say so once. An invisible fallback here is indistinguishable
  // from a correctly-applied default, which is how this went unnoticed.
  if (!warned) {
    warned = true;
    console.warn(
      '[token-masks] could not read the entity token masks; generated tokens will use ' +
        "the built-in pattern and may not match this instance's configured mask",
    );
  }
  return null;
}

/**
 * Drop the memoised masks so the next read goes back to the server.
 *
 * Called by the admin settings screen after any settings write — the console edits
 * `entity.token_masks` itself, so "the masks change about never" is true of an
 * instance but NOT of this tab. Without it, saving a new mask and then opening a
 * create form shows the previous one as though it were the operator's.
 */
export function forgetCachedSettings() {
  cache = null;
  warned = false;
}

/** Alias for tests, which reset for isolation rather than for a settings write. */
export const resetTokenMasksCache = forgetCachedSettings;
