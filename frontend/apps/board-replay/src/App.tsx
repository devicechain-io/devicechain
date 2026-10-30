// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The replay page: load the recording and its board, then play it.
//
// The header (the replay label and the provenance toggle) is rendered by THIS component for
// every state, so there is no branch below that can forget it. The body is one of: loading,
// an error, or the player.

import {
  RecordedDataSource,
  createRecordedClock,
  createRecordedLister,
  createRecordedResolver,
  type ClockTicker,
  type RecordedClock,
  type WidgetDataSource,
} from '@devicechain/dashboards';
import { lazy, Suspense, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';

import type { WidgetRegistry } from '@devicechain/host-widgets';
import type { EmbedOptions } from './embed';
import type { ReplayTheme } from './host-options';
import { loadReplay, ReplayLoadError, type LoadedReplay } from './load';
import { ProvenancePanel, ReplayHeader } from './ReplayHeader';
import { TransportBar, replayState, useClockView } from './TransportBar';

type Load =
  | { status: 'loading' }
  | { status: 'error'; error: ReplayLoadError | null }
  | { status: 'ready'; replay: LoadedReplay };

export interface ReplayAppProps {
  recordingUrl: string | null | undefined;
  boardUrl: string | null | undefined;
  // Test seams: the fetch to use, the page origin, and the clock's frame source.
  fetchFn?: typeof fetch;
  origin?: string;
  ticker?: ClockTicker;
  // The widget registry to parse and render with; defaults to the deployment's.
  registry?: WidgetRegistry;
  // The embed mode (embed.ts), or nothing for the full replay page.
  embed?: EmbedOptions | null;
  theme?: ReplayTheme;
}

// The embed is loaded only in embed mode. mount.tsx fetches it (loadEmbed) before it renders an
// embed, so by the first render this resolves at once and the label is on the first frame.
const Board = lazy(() => import('./Board').then((m) => ({ default: m.Board })));

export const loadEmbed = () => import('./EmbedPlayer');
const EmbedView = lazy(loadEmbed);

export default function ReplayApp({ recordingUrl, boardUrl, fetchFn, origin, ticker, registry, embed, theme = 'dark' }: ReplayAppProps) {
  const [load, setLoad] = useState<Load>({ status: 'loading' });
  const [provenanceOpen, setProvenanceOpen] = useState(false);
  const [rootEl, setRootEl] = useState<HTMLElement | null>(null);
  const { i18n } = useTranslation();

  useEffect(() => {
    let cancelled = false;
    setLoad({ status: 'loading' });
    loadReplay({ recordingUrl, boardUrl, fetchFn, origin, registry }).then(
      (replay) => {
        if (!cancelled) setLoad({ status: 'ready', replay });
      },
      (err: unknown) => {
        if (!cancelled) setLoad({ status: 'error', error: err instanceof ReplayLoadError ? err : null });
      },
    );
    return () => {
      cancelled = true;
    };
    // The inputs are read once per mount: a different recording is a different page.
  }, [recordingUrl, boardUrl]);

  const recording = load.status === 'ready' ? load.replay.recording : null;
  return (
    <div
      ref={setRootEl}
      className="board-replay-root"
      lang={i18n.language}
      data-theme={theme}
      data-embed={embed ? '' : undefined}
      style={embed ? { minHeight: 0 } : { display: 'flex', flexDirection: 'column' }}
      data-testid="replay-root"
    >
      {embed ? (
        // The embed: the replay label and the board on a fixed canvas, and nothing else.
        <Suspense fallback={null}>
          <EmbedView
            replay={load.status === 'ready' ? load.replay : null}
            options={embed}
            ticker={ticker}
            themeRoot={rootEl ?? undefined}
            pending={load.status === 'loading' ? <StatusBody /> : load.status === 'error' && <ErrorBody error={load.error} />}
          />
        </Suspense>
      ) : (
        <>
          <ReplayHeader
            recording={recording}
            provenanceOpen={provenanceOpen}
            onToggleProvenance={() => setProvenanceOpen((v) => !v)}
          />
          {recording && provenanceOpen && <ProvenancePanel recording={recording} />}
          {load.status === 'loading' && <StatusBody />}
          {load.status === 'error' && <ErrorBody error={load.error} />}
          {load.status === 'ready' && <Player replay={load.replay} ticker={ticker} themeRoot={rootEl ?? undefined} />}
        </>
      )}
    </div>
  );
}

function StatusBody() {
  const { t } = useTranslation();
  return (
    <main
      data-testid="replay-body"
      data-state="loading"
      style={{ flex: '1 1 auto', display: 'grid', placeItems: 'center' }}
    >
      <div role="status">{t('loading')}</div>
    </main>
  );
}

function ErrorBody({ error }: { error: ReplayLoadError | null }) {
  const { t } = useTranslation();
  const detail = error?.detail ?? t('errorDetailUnknown');
  const message = (() => {
    switch (error?.code) {
      case 'missingUrl':
        return t('errorMissingUrl');
      case 'crossOrigin':
        return t('errorCrossOrigin');
      case 'fetch':
        return t('errorFetch', { detail });
      case 'recordingInvalid':
        return t('errorRecordingInvalid', { detail });
      case 'boardInvalid':
        return t('errorBoardInvalid', { detail });
      case 'boardTooNew':
        // The words were fetched on demand by loadReplay and ride in `detail`.
        return detail;
      case 'boardMismatch':
        return t('errorBoardMismatch');
      default:
        return t('errorUnexpected');
    }
  })();
  return (
    <main
      data-testid="replay-body"
      data-state="error"
      style={{ flex: '1 1 auto', display: 'grid', placeItems: 'center', padding: 16, textAlign: 'center' }}
    >
      <div role="alert">
        <div style={{ fontWeight: 600, marginBottom: 4 }}>{t('errorTitle')}</div>
        <div>{message}</div>
      </div>
    </main>
  );
}

// The player owns the clock and the CURRENT data source. A seek retires a source (widget
// streams only append, so a backwards jump cannot be retracted through one), and the player
// builds a new one at the new cursor; the board, which holds the selection, stays mounted.
function Player({
  replay,
  ticker,
  themeRoot,
}: {
  replay: LoadedReplay;
  ticker?: ClockTicker;
  themeRoot: Element | undefined;
}) {
  const { recording, definition, registry, siteAnchors } = replay;
  const clock: RecordedClock = useMemo(() => createRecordedClock(recording, { ticker }), [recording, ticker]);
  const site = useMemo(() => ({ siteAnchors }), [siteAnchors]);
  const resolver = useMemo(() => createRecordedResolver(recording, site), [recording, site]);
  const lister = useMemo(() => createRecordedLister(recording, site), [recording, site]);
  const [source, setSource] = useState<WidgetDataSource>(() => new RecordedDataSource(recording, clock, site));
  const [seeking, setSeeking] = useState(false);
  const view = useClockView(clock);

  useEffect(() => {
    const off = clock.subscribe((ev) => {
      if (ev.reason === 'seek') setSource(new RecordedDataSource(recording, clock, site));
    });
    return () => {
      off();
      clock.pause();
    };
  }, [clock, recording, site]);

  return (
    <>
      <main
        data-testid="replay-body"
        data-state={replayState(view, seeking)}
        style={{ flex: '1 1 auto', minHeight: 0, overflow: 'auto' }}
      >
        <Suspense fallback={null}>
          <Board definition={definition} registry={registry} source={source} resolver={resolver} lister={lister} themeRoot={themeRoot} />
        </Suspense>
      </main>
      <TransportBar clock={clock} recording={recording} seeking={seeking} onSeeking={setSeeking} />
    </>
  );
}
