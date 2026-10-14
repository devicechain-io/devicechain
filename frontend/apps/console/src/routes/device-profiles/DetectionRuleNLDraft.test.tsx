// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// When drafting is unavailable the notice must lead with the console's TRANSLATED message.
// The server's reason is English prose; it used to replace the translation outright.
import '@/i18n/config';
import i18n from 'i18next';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { draft } = vi.hoisted(() => ({ draft: vi.fn() }));
vi.mock('@/lib/api/event-processing', () => ({ draftDetectionRuleFromText: draft }));
vi.mock('@/lib/api/device-management', () => ({ listMetricDefinitions: () => Promise.resolve([]) }));

import { DetectionRuleNLDraft } from './DetectionRuleNLDraft';

const unavailable = (reason: string | null) => ({
  ok: false,
  unavailable: true,
  unavailableReason: reason,
  diagnostics: [],
  warnings: [],
  attempts: 0,
});

async function submitDescription() {
  render(<DetectionRuleNLDraft profileToken="p" onDrafted={vi.fn()} />);
  fireEvent.change(screen.getByRole('textbox'), { target: { value: 'alert when hot' } });
  fireEvent.click(screen.getByRole('button', { name: /draft rule|redactar|borrador/i }));
  await waitFor(() => expect(document.querySelector('[data-shot="nl-unavailable"]')).not.toBeNull());
}

beforeEach(() => draft.mockReset());
afterEach(async () => {
  cleanup();
  await i18n.changeLanguage('en');
});

describe('the unavailable notice', () => {
  it('shows the translated message first and keeps the server reason as secondary detail', async () => {
    await i18n.changeLanguage('es');
    draft.mockResolvedValue(unavailable('No model granted to this tenant.'));
    await submitDescription();

    const notice = document.querySelector('[data-shot="nl-unavailable"]')!;
    expect(notice.textContent).toContain('La redacción en lenguaje natural no está disponible');
    expect(screen.getByTestId('nl-unavailable-detail').textContent).toBe(
      'Detalle del servidor: No model granted to this tenant.',
    );
    expect(notice.firstElementChild!.textContent).not.toContain('No model granted');
  });

  it('shows no detail line when the server gave no reason', async () => {
    draft.mockResolvedValue(unavailable(null));
    await submitDescription();
    expect(screen.queryByTestId('nl-unavailable-detail')).toBeNull();
  });
});
