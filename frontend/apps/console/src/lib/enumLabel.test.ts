// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import '@/i18n/config';
import i18n from 'i18next';
import { afterEach, describe, expect, it } from 'vitest';
import { COMMAND_STATUSES } from '@devicechain/dashboards';
import { artifactKindLabel, commandStatusLabel, signerStateLabel, updateStateLabel } from './enumLabel';

afterEach(() => i18n.changeLanguage('en'));

const UPDATE_STATES = [
  'QUEUED', 'INITIATED', 'DOWNLOADING', 'DOWNLOADED', 'VERIFIED', 'UPDATING',
  'REBOOTING', 'UPDATED', 'FAILED', 'TIMED_OUT', 'CANCELLED', 'UNKNOWN',
];

describe('enumLabel', () => {
  it('localises every command status and update state in es (never the raw enum)', async () => {
    await i18n.changeLanguage('es');
    const t = i18n.t.bind(i18n);
    for (const s of COMMAND_STATUSES) expect(commandStatusLabel(t, s), s).not.toBe(s);
    for (const s of UPDATE_STATES) expect(updateStateLabel(t, s), s).not.toBe(s);
    expect(signerStateLabel(t, 'ACTIVE')).toBe('Activo');
    expect(artifactKindLabel(t, 'CONFIGURATION')).toBe('Configuración');
  });

  it('renders English labels in en', () => {
    expect(commandStatusLabel(i18n.t.bind(i18n), 'TIMEOUT')).toBe('Timed out');
  });

  it('shows an unknown value verbatim', () => {
    expect(commandStatusLabel(i18n.t.bind(i18n), 'SOMETHING_NEW')).toBe('SOMETHING_NEW');
  });
});
