// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// 🔴 What a create form SAYS when it could not read the instance's token masks.
//
// The masks are advisory — the backend enforces only the grammar — so falling back to
// the built-in pattern is the right action, and blocking the form would be worse. The
// defect was never the fallback. It was that the fallback was indistinguishable from
// success: the field went on showing "Pattern: {slug}" as though that were the
// operator's configuration, and the regenerate button went on minting tokens from it.
//
// So the contract here is narrow and specific: the same token is still generated, and
// the field stops claiming the pattern is authoritative.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { masks, auth } = vi.hoisted(() => ({
  masks: { value: null as Record<string, string> | null },
  auth: { isAuthenticated: true, isIdentityAuthenticated: false },
}));

vi.mock('@/lib/token-masks', () => ({ getTokenMasks: () => Promise.resolve(masks.value) }));
vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => auth }));

import { TokenField } from './token-field';

afterEach(cleanup);
beforeEach(() => {
  masks.value = null;
});

function renderField(onChange = vi.fn()) {
  render(<TokenField entityType="device" value="" onChange={onChange} seed="Pump 4" />);
  return onChange;
}

const UNAVAILABLE = /Could not read this instance's token pattern/;

describe('when the masks could not be read', () => {
  it('says the pattern is the built-in one, not this instance’s', async () => {
    renderField();
    expect(await screen.findByText(UNAVAILABLE)).toBeTruthy();
  });

  it('does not show the pattern hint as though it were authoritative', async () => {
    renderField();
    await screen.findByText(UNAVAILABLE);
    expect(screen.queryByText(/^Pattern:/)).toBeNull();
  });

  // The whole point of not blocking. A degraded read must not cost the operator the
  // button — that would trade a silent wrong value for a broken form, which is worse.
  it('still generates a token from the built-in pattern', async () => {
    const onChange = renderField();
    await screen.findByText(UNAVAILABLE);

    fireEvent.click(screen.getByRole('button', { name: 'Generate a token from the mask' }));
    await waitFor(() => expect(onChange).toHaveBeenCalled());
    expect(onChange.mock.calls[0][0]).toBeTruthy();
  });
});

// 🔴 The counterweight, and the distinction the whole slice turns on. An operator who
// configured NOTHING gets `{}` — and the built-in pattern genuinely IS this instance's
// pattern, so warning would be false. Without this test, a component that showed the
// warning unconditionally would pass every case above.
describe('when the operator configured no masks', () => {
  it('shows the pattern and no warning', async () => {
    masks.value = {};
    renderField();

    expect(await screen.findByText(/^Pattern:/)).toBeTruthy();
    expect(screen.queryByText(UNAVAILABLE)).toBeNull();
  });
});

describe('when the masks were read', () => {
  it('shows the operator’s configured pattern', async () => {
    masks.value = { device: 'dev-{alphanumeric-4}' };
    renderField();

    expect(await screen.findByText(/dev-\{alphanumeric-4\}/)).toBeTruthy();
    expect(screen.queryByText(UNAVAILABLE)).toBeNull();
  });
});
