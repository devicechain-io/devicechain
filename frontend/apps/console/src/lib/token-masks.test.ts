// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// 🔴 The masks used to be read over ONE lane — the identity lane, because that is
// where the settings schema lives. The identity token expires in fifteen minutes and
// cannot refresh; a tenant session lasts up to seven days. So a tenant user a quarter
// of an hour into a session opened a create form, the query 401'd, the failure was
// swallowed into `{}`, and every token from then on used the built-in pattern instead
// of the operator's — silently, because a plausible token looks like a correct one.
//
// These tests pin the three things that fix requires, none of which the old code could
// express: the lane is chosen by which SESSION is live, a refusal on one lane means
// try the other, and every lane failing produces "unknown" rather than "empty".
import { beforeEach, describe, expect, it, vi } from 'vitest';

const { tenantLane, identityLane } = vi.hoisted(() => ({
  tenantLane: vi.fn(),
  identityLane: vi.fn(),
}));

vi.mock('@/lib/api/user-management', () => ({ getTenantTokenMasks: tenantLane }));
vi.mock('@/lib/api/settings', () => ({ getIdentityTokenMasks: identityLane }));

import { getTokenMasks, resetTokenMasksCache } from './token-masks';

const CONFIGURED = '{"device":"dev-{alphanumeric-4}"}';
const TENANT_SCREEN = { isAuthenticated: true, isIdentityAuthenticated: true };
const ADMIN_SCREEN = { isAuthenticated: false, isIdentityAuthenticated: true };

beforeEach(() => {
  resetTokenMasksCache();
  tenantLane.mockReset();
  identityLane.mockReset();
  // 🔑 mockClear is not tidiness. vi.spyOn on an already-spied method hands back the
  // SAME spy, so without clearing, its call history accumulates across the file and
  // the "did not warn" assertion below silently measures every preceding test instead
  // of this one — it failed on warnings three other cases had emitted.
  vi.spyOn(console, 'warn').mockImplementation(() => {}).mockClear();
});

describe('choosing a lane', () => {
  // Both sessions are live here — the ordinary state of a tenant screen in its first
  // fifteen minutes — so a test that merely checked "the masks were read" would pass
  // against the broken code too. This pins the preference: the long-lived session is
  // asked, and the short-lived one is left alone.
  //
  // 🔑 The preference is a preference, not the fix. What repairs the original defect is
  // that a tenant lane exists at all, and what stops the expireIdentity() side effect is
  // the isIdentityAuthenticated guard (already false once that token lapses) — see
  // 'answers null when no session is live at all', which is the case that pins it.
  it('reads over the tenant session and never touches the identity lane', async () => {
    tenantLane.mockResolvedValue(CONFIGURED);
    identityLane.mockResolvedValue('{"device":"WRONG"}');

    expect(await getTokenMasks(TENANT_SCREEN)).toEqual({ device: 'dev-{alphanumeric-4}' });
    expect(tenantLane).toHaveBeenCalledTimes(1);
    expect(identityLane).not.toHaveBeenCalled();
  });

  // The counterweight, and the case that refuted the first version of this fix. An
  // operator creating the instance's FIRST tenant holds an identity session and no
  // tenant session at all. Moving the masks to the tenant plane alone would have
  // replaced a fifteen-minute bug with a permanent one on the bootstrap path.
  it('reads over the identity session when there is no tenant session', async () => {
    identityLane.mockResolvedValue(CONFIGURED);

    expect(await getTokenMasks(ADMIN_SCREEN)).toEqual({ device: 'dev-{alphanumeric-4}' });
    expect(tenantLane).not.toHaveBeenCalled();
    expect(identityLane).toHaveBeenCalledTimes(1);
  });

  it('falls back to the identity lane when the tenant lane refuses', async () => {
    tenantLane.mockRejectedValue(new Error('401'));
    identityLane.mockResolvedValue(CONFIGURED);

    expect(await getTokenMasks(TENANT_SCREEN)).toEqual({ device: 'dev-{alphanumeric-4}' });
    expect(identityLane).toHaveBeenCalledTimes(1);
  });
});

describe('when the masks cannot be read', () => {
  // 🔴 The distinction the old code destroyed. `{}` means the operator configured
  // nothing, so the built-in pattern IS the truth; `null` means nobody knows, so the
  // built-in pattern is a guess. Returning `{}` for a failure made a guess
  // indistinguishable from the truth at every call site.
  it('answers null rather than an empty map', async () => {
    tenantLane.mockRejectedValue(new Error('401'));
    identityLane.mockRejectedValue(new Error('401'));

    expect(await getTokenMasks(TENANT_SCREEN)).toBeNull();
  });

  it('says so, because an invisible fallback is a control that cannot fail', async () => {
    tenantLane.mockRejectedValue(new Error('401'));
    identityLane.mockRejectedValue(new Error('401'));

    await getTokenMasks(TENANT_SCREEN);
    expect(console.warn).toHaveBeenCalledWith(expect.stringContaining('[token-masks]'));
  });

  it('answers null when no session is live at all', async () => {
    expect(await getTokenMasks({ isAuthenticated: false, isIdentityAuthenticated: false })).toBeNull();
    expect(tenantLane).not.toHaveBeenCalled();
    expect(identityLane).not.toHaveBeenCalled();
  });

  // The positive control for the pair above: an operator who really has configured
  // nothing gets `{}`, which is NOT the failure value. Without this, returning null
  // unconditionally would satisfy both tests above.
  it('answers an empty map — not null — when nothing is configured', async () => {
    tenantLane.mockResolvedValue('{}');

    expect(await getTokenMasks(TENANT_SCREEN)).toEqual({});
    expect(console.warn).not.toHaveBeenCalled();
  });
});

describe('caching', () => {
  it('reads once and reuses the answer', async () => {
    tenantLane.mockResolvedValue(CONFIGURED);

    await getTokenMasks(TENANT_SCREEN);
    await getTokenMasks(TENANT_SCREEN);
    expect(tenantLane).toHaveBeenCalledTimes(1);
  });

  // A failure must not poison the session: the usual cause is one lane being briefly
  // unavailable, and a create form opened a minute later should get the real masks.
  it('does not cache a failure', async () => {
    tenantLane.mockRejectedValueOnce(new Error('401')).mockResolvedValue(CONFIGURED);
    identityLane.mockRejectedValueOnce(new Error('401'));

    expect(await getTokenMasks(TENANT_SCREEN)).toBeNull();
    expect(await getTokenMasks(TENANT_SCREEN)).toEqual({ device: 'dev-{alphanumeric-4}' });
  });
});
