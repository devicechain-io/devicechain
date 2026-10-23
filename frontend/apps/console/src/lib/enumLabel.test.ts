// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import '@/i18n/config';
import i18n from 'i18next';
import { afterEach, describe, expect, it } from 'vitest';
import { COMMAND_STATUSES } from '@devicechain/dashboards';
import en from '@/i18n/locales/en/common.json';
import es from '@/i18n/locales/es/common.json';
import type {
  ArtifactKind,
  ArtifactState,
  SignerState,
  UpdateRolloutOutcome,
  UpdateRolloutPauseReason,
  UpdateRolloutState,
  UpdateState,
} from '@/lib/api/update-management';
import { artifactKindLabel, commandStatusLabel, signerStateLabel, updateStateLabel } from './enumLabel';

afterEach(() => i18n.changeLanguage('en'));

// Every member of each codegen'd schema enum, held to it by `satisfies`: a value the schema
// adds is a missing key here (typecheck fails), a value it drops is an excess one. The
// runtime fallback renders an unknown value verbatim, so without this a new state would ship
// as a raw enum in every badge and no test would notice.
const members = <T extends string>(o: Record<T, true>) => Object.keys(o) as T[];
const UPDATE_STATES = members({
  QUEUED: true, INITIATED: true, DOWNLOADING: true, DOWNLOADED: true, VERIFIED: true, UPDATING: true,
  REBOOTING: true, UPDATED: true, FAILED: true, TIMED_OUT: true, CANCELLED: true, UNKNOWN: true,
} satisfies Record<UpdateState, true>);
const ARTIFACT_KINDS = members({ FIRMWARE: true, SOFTWARE: true, CONFIGURATION: true } satisfies Record<ArtifactKind, true>);
const ARTIFACT_STATES = members({ UPLOADING: true, AVAILABLE: true, RETIRED: true } satisfies Record<ArtifactState, true>);
const SIGNER_STATES = members({ ACTIVE: true, REVOKED: true } satisfies Record<SignerState, true>);
const ROLLOUT_STATES = members({
  DRAFT: true, RUNNING: true, PAUSED: true, COMPLETED: true, ABORTED: true,
} satisfies Record<UpdateRolloutState, true>);
const ROLLOUT_OUTCOMES = members({
  PENDING: true, IN_FLIGHT: true, SUCCEEDED: true, FAILED: true, UNKNOWN: true, CANCELLED: true, SKIPPED: true,
} satisfies Record<UpdateRolloutOutcome, true>);
const ROLLOUT_PAUSE_REASONS = members({
  OPERATOR: true, FAILURE_THRESHOLD: true, ARTIFACT_NOT_ASSIGNABLE: true,
} satisfies Record<UpdateRolloutPauseReason, true>);

const FAMILIES: Array<[string, readonly string[]]> = [
  // Command status is a String on the wire; COMMAND_STATUSES is the console's closed set of it.
  ['commandStatus', COMMAND_STATUSES],
  ['updateState', UPDATE_STATES],
  ['artifactKind', ARTIFACT_KINDS],
  ['artifactState', ARTIFACT_STATES],
  ['signerState', SIGNER_STATES],
  ['rolloutState', ROLLOUT_STATES],
  ['rolloutOutcome', ROLLOUT_OUTCOMES],
  ['rolloutPauseReason', ROLLOUT_PAUSE_REASONS],
];

describe('enumLabel', () => {
  it('has an en and es key for every value of every enum family (not just the fallback)', () => {
    for (const [prefix, values] of FAMILIES) {
      for (const v of values) {
        for (const [name, cat] of [['en', en], ['es', es]] as const) {
          expect((cat as Record<string, string>)[`${prefix}_${v}`], `${name} ${prefix}_${v}`).toBeTruthy();
        }
      }
    }
  });

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
