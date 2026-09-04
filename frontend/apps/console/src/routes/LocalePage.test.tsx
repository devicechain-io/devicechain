// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The tenant default-language editor.
//
// What is faked here is the SEAM around the page — the tenant cache, the auth claims,
// and the mutation — and nothing else. The locale registry and the catalogs are real,
// because the segment labels come from the registry rather than from a catalog and a
// fake registry would prove nothing about what an operator sees.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { authorities, tenantState, setTenantLocaleMock, applyTenantMock, toastMock } = vi.hoisted(
  () => ({
    authorities: { value: ['locale:write'] as string[] },
    tenantState: {
      value: null as null | {
        token: string;
        locale: string | null;
        localeOverride: string | null;
      },
    },
    setTenantLocaleMock: vi.fn(),
    applyTenantMock: vi.fn(),
    toastMock: vi.fn(),
  }),
);

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { authorities: authorities.value } }),
}));

vi.mock('@/auth/TenantProvider', () => ({
  useCurrentTenant: () => tenantState.value,
  useSetCurrentTenant: () => applyTenantMock,
}));

vi.mock('@/lib/api/user-management', () => ({
  setTenantLocale: (...args: unknown[]) => setTenantLocaleMock(...args),
}));

vi.mock('@/components/ui/toast', () => ({
  useToast: () => ({ toast: toastMock }),
}));

import LocalePage from './LocalePage';

/** An inheriting tenant that picks up English from the operator's instance default. */
function inheritingTenant() {
  return { token: 'acme', locale: 'en', localeOverride: null };
}

afterEach(cleanup);
beforeEach(() => {
  authorities.value = ['locale:write'];
  tenantState.value = inheritingTenant();
  setTenantLocaleMock.mockReset();
  setTenantLocaleMock.mockResolvedValue({ token: 'acme' });
  applyTenantMock.mockReset();
  toastMock.mockReset();
});

const saveButton = () => screen.getByRole('button', { name: /save language/i }) as HTMLButtonElement;
const segment = (name: RegExp) => screen.getByRole('radio', { name });

describe('authority', () => {
  // 🔴 Neither neighbouring grant may open this page. locale:write exists because this
  // one value re-languages the console for every member who has not chosen otherwise —
  // a different act from restyling the shell or configuring a map.
  it('refuses a holder of branding:write alone', () => {
    authorities.value = ['branding:write'];
    render(<LocalePage />);

    expect(screen.queryByText('locale:write')).toBeTruthy();
    expect(screen.queryByRole('radiogroup')).toBeNull();
  });

  it('refuses a holder of basemap:write alone', () => {
    authorities.value = ['basemap:write'];
    render(<LocalePage />);

    expect(screen.queryByRole('radiogroup')).toBeNull();
  });

  it('admits a holder of locale:write', () => {
    render(<LocalePage />);
    expect(screen.getByRole('radiogroup')).toBeTruthy();
  });
});

describe('seeding', () => {
  // 🔴 The gate is the TENANT, not the override. A null override is a legitimate
  // steady state here (it is what "inherit" looks like), so unlike branding and
  // basemap it cannot double as the not-yet-fetched signal — seeding from a tenant
  // that has not landed would show Inherit to a tenant that has a language set, and a
  // save on that would clear it.
  it('shows a loading state rather than a form before the tenant arrives', () => {
    tenantState.value = null;
    render(<LocalePage />);

    expect(screen.queryByRole('radiogroup')).toBeNull();
  });

  it('seeds the tenant own choice when it has one', () => {
    tenantState.value = { token: 'acme', locale: 'es', localeOverride: 'es' };
    render(<LocalePage />);

    expect(segment(/Español/).getAttribute('aria-checked')).toBe('true');
    expect(segment(/Inherit/).getAttribute('aria-checked')).toBe('false');
  });

  it('seeds Inherit when the tenant stores no override', () => {
    render(<LocalePage />);

    expect(segment(/Inherit/).getAttribute('aria-checked')).toBe('true');
  });
});

describe('saving', () => {
  it('sends the chosen tag', async () => {
    render(<LocalePage />);
    fireEvent.click(segment(/Español/));
    fireEvent.click(saveButton());

    await waitFor(() => expect(setTenantLocaleMock).toHaveBeenCalledTimes(1));
    expect(setTenantLocaleMock.mock.calls[0][0]).toBe('es');
  });

  // 🔴 Choosing Inherit must send an explicit NULL. The sentinel is a UI-only value —
  // sending the literal string "inherit" would be stored as a language tag if the
  // server took it, and refused as one if it did not; neither is "clear this".
  it('clears the override with an explicit null rather than with the sentinel', async () => {
    tenantState.value = { token: 'acme', locale: 'es', localeOverride: 'es' };
    render(<LocalePage />);
    fireEvent.click(segment(/Inherit/));
    fireEvent.click(saveButton());

    await waitFor(() => expect(setTenantLocaleMock).toHaveBeenCalledTimes(1));
    expect(setTenantLocaleMock.mock.calls[0][0]).toBeNull();
  });

  it('writes the fresh tenant into the cache so the shell re-languages without a refetch', async () => {
    const fresh = { token: 'acme' };
    setTenantLocaleMock.mockResolvedValue(fresh);
    render(<LocalePage />);
    fireEvent.click(segment(/Español/));
    fireEvent.click(saveButton());

    await waitFor(() => expect(applyTenantMock).toHaveBeenCalledWith(fresh));
  });

  it('surfaces a server refusal instead of reporting success', async () => {
    setTenantLocaleMock.mockRejectedValue(new Error('locale "zz-top" is not a language tag'));
    render(<LocalePage />);
    fireEvent.click(segment(/Español/));
    fireEvent.click(saveButton());

    await waitFor(() => expect(screen.getByText(/is not a language tag/)).toBeTruthy());
    expect(toastMock).not.toHaveBeenCalled();
  });
});

describe('inheritance', () => {
  it('says which instance default is in force when this tenant sets none', () => {
    render(<LocalePage />);

    // Named by its ENDONYM, which is what the switcher shows and what a reader
    // recognizes — not the raw tag.
    expect(screen.getByTestId('locale-inheriting').textContent).toContain('English');
  });

  // 🔴 tenant.locale is the EFFECTIVE value and already includes this tenant's own
  // override. Gating the hint on the selection rather than on the STORED override
  // would tell a tenant that is switching to Inherit that it is "inheriting the
  // instance default: <its own language>" — wrong twice: not the instance default, and
  // not what saving would keep.
  it('does not present the tenant own language as the instance default while clearing it', () => {
    tenantState.value = { token: 'acme', locale: 'es', localeOverride: 'es' };
    render(<LocalePage />);
    fireEvent.click(segment(/Inherit/));

    expect(screen.queryByTestId('locale-inheriting')).toBeNull();
  });

  it('says nothing about inheriting once this tenant has its own', () => {
    tenantState.value = { token: 'acme', locale: 'es', localeOverride: 'es' };
    render(<LocalePage />);

    expect(screen.queryByTestId('locale-inheriting')).toBeNull();
  });
});

// The precedence note is on the screen because the question it answers is asked at
// exactly this moment: "I set the tenant to Spanish and my colleague still sees
// English." Dropping it is a silent regression in a support burden, so it is pinned.
describe('the precedence note', () => {
  it('tells the reader that an explicit user choice wins', () => {
    render(<LocalePage />);
    expect(screen.getByText(/picked a language from the switcher keeps it/i)).toBeTruthy();
  });
});
