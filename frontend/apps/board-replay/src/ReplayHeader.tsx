// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { BoardRecording } from '@devicechain/dashboards';
import { useTranslation } from 'react-i18next';

import { formatRecordedInstant, formatRunDate } from './format';

// The permanent label. It renders in EVERY state (loading, error, playing, paused,
// seeking, ended) and sits outside the board's scroll area, so there is no moment at which
// the page shows recorded values without saying that they are a recording.
//
// Before the recording has loaded (or when it fails to) the date and run id are unknown, so
// only the fixed phrase shows; once it is parsed the date comes from its own header, never
// from the clock of the machine viewing it.
export function ReplayHeader({
  recording,
  provenanceOpen,
  onToggleProvenance,
}: {
  recording: BoardRecording | null;
  provenanceOpen: boolean;
  onToggleProvenance: () => void;
}) {
  const { t, i18n } = useTranslation();
  const label = recording
    ? t('replayLabel', { date: formatRunDate(recording, i18n.language), runId: recording.runId })
    : t('replayBadge');
  return (
    <header
      style={{
        display: 'flex',
        alignItems: 'center',
        gap: 12,
        padding: '8px 16px',
        borderBottom: '1px solid hsl(var(--border))',
        flex: '0 0 auto',
      }}
    >
      <div data-testid="replay-label" style={{ flex: '1 1 auto', fontWeight: 600 }}>
        {label}
      </div>
      {recording && (
        <button
          type="button"
          onClick={onToggleProvenance}
          aria-expanded={provenanceOpen}
          style={buttonStyle}
        >
          {provenanceOpen ? t('provenanceHide') : t('provenanceShow')}
        </button>
      )}
    </header>
  );
}

export const buttonStyle = {
  padding: '4px 10px',
  borderRadius: 6,
  border: '1px solid hsl(var(--input))',
  background: 'transparent',
  color: 'inherit',
  font: 'inherit',
  cursor: 'pointer',
} as const;

// The provenance panel: everything a viewer needs to judge what they are looking at.
export function ProvenancePanel({ recording }: { recording: BoardRecording }) {
  const { t, i18n } = useTranslation();
  const rows: Array<[string, string]> = [
    [t('provenanceRunId'), recording.runId],
    [t('provenanceRecorded'), `${formatRecordedInstant(recording, i18n.language)} UTC`],
    [t('provenancePlatform'), recording.platformVersion],
    [t('provenanceBuild'), `${recording.build.gitSha} / ${recording.build.sdkCommit}`],
    [t('provenanceBoard'), recording.board.path],
    [t('provenanceBoardCommit'), recording.board.sourceCommit],
    [t('provenanceBoardHash'), recording.board.sha256],
    [t('provenanceConverter'), String(recording.converter.version)],
  ];
  if (recording.excerpt) {
    rows.push([
      t('provenanceExcerpt'),
      `${recording.excerpt.fromMs / 1000} s – ${recording.excerpt.toMs / 1000} s`,
    ]);
  }
  const sources = Object.entries(recording.sourceHashes);
  return (
    <section
      aria-label={t('provenanceTitle')}
      data-testid="provenance-panel"
      style={{
        padding: '8px 16px',
        borderBottom: '1px solid hsl(var(--border))',
        background: 'hsl(var(--card))',
        maxHeight: '40%',
        overflow: 'auto',
        flex: '0 0 auto',
        fontSize: 13,
      }}
    >
      <p style={{ margin: '0 0 4px' }}>{t('provenanceRecordedAgainst')}</p>
      <p style={{ margin: '0 0 4px' }}>{t('provenanceMachines', { count: recording.devices.length })}</p>
      <p style={{ margin: '0 0 4px' }}>{t('provenanceValues')}</p>
      <p style={{ margin: '0 0 8px' }}>{t('widgetsEnglish')}</p>
      <dl style={{ margin: 0, display: 'grid', gridTemplateColumns: 'max-content 1fr', gap: '2px 12px' }}>
        {rows.map(([k, v]) => (
          <div key={k} style={{ display: 'contents' }}>
            <dt style={{ color: 'hsl(var(--muted-foreground))' }}>{k}</dt>
            <dd style={{ margin: 0, overflowWrap: 'anywhere' }}>{v}</dd>
          </div>
        ))}
      </dl>
      {sources.length > 0 && (
        <>
          <div style={{ margin: '8px 0 2px', color: 'hsl(var(--muted-foreground))' }}>{t('provenanceSources')}</div>
          <dl style={{ margin: 0, display: 'grid', gridTemplateColumns: 'max-content 1fr', gap: '2px 12px' }}>
            {sources.map(([file, hash]) => (
              <div key={file} style={{ display: 'contents' }}>
                <dt>{file}</dt>
                <dd style={{ margin: 0, overflowWrap: 'anywhere' }}>{hash}</dd>
              </div>
            ))}
          </dl>
        </>
      )}
    </section>
  );
}
