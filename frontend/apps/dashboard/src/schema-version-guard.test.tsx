// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A board saved by a newer release is REFUSED in the viewer, in words, on both doors (paste
// and by-token). The real Load screen runs; only the transport is faked. The measured claim
// is that the viewer shows the refusal and never hands a board to the renderer.

import { CURRENT_SCHEMA_VERSION } from '@devicechain/dashboards';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({ definition: '' }));
vi.mock('@devicechain/client', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  gql: async () => ({ publishedDashboard: { definition: h.definition } }),
}));

import { Load } from './App';
import i18n from './i18n/config';

const newer = JSON.stringify({ schemaVersion: CURRENT_SCHEMA_VERSION + 1, title: 'T', widgets: [] });
const current = JSON.stringify({ schemaVersion: CURRENT_SCHEMA_VERSION, title: 'T', widgets: [] });

beforeEach(async () => {
  await i18n.changeLanguage('en');
});
afterEach(cleanup);

describe('/dash with a board from a newer version', () => {
  // The paste door awaits the board's lazily loaded widget code before it shows anything, so both
  // outcomes below are awaited rather than read synchronously after the click.
  it('shows the refusal for a pasted definition and renders nothing', async () => {
    const onRender = vi.fn();
    render(<Load onRender={onRender} onSignOut={() => {}} />);
    fireEvent.change(screen.getByLabelText(i18n.t('load:definitionLabel')), { target: { value: newer } });
    fireEvent.click(screen.getByText(i18n.t('load:render')));
    expect(await screen.findByText(i18n.t('load:errorDefinitionTooNew'))).toBeTruthy();
    expect(i18n.t('load:errorDefinitionTooNew')).toMatch(/newer version of DeviceChain.*Upgrade this viewer/);
    expect(onRender).not.toHaveBeenCalled();
  });

  it('shows the refusal, translated, for the published snapshot by token', async () => {
    await i18n.changeLanguage('es');
    h.definition = newer;
    const onRender = vi.fn();
    render(<Load onRender={onRender} onSignOut={() => {}} autoLoadToken="ops" />);
    const message = i18n.t('load:errorDefinitionTooNew');
    expect(message).toMatch(/versión más reciente/);
    await waitFor(() => expect(screen.getByText(message)).toBeTruthy());
    expect(onRender).not.toHaveBeenCalled();
  });

  it('still renders a board at the current version', async () => {
    const onRender = vi.fn();
    render(<Load onRender={onRender} onSignOut={() => {}} />);
    fireEvent.change(screen.getByLabelText(i18n.t('load:definitionLabel')), { target: { value: current } });
    fireEvent.click(screen.getByText(i18n.t('load:render')));
    await waitFor(() => expect(onRender).toHaveBeenCalledTimes(1));
  });
});
