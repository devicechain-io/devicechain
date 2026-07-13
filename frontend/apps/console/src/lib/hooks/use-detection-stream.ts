// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useRef, useState } from 'react';
import { subscribe, type SubscriptionSink } from '@devicechain/client';
import { DETECTION_STREAM, type DetectionEvent, type DetectionStreamData } from '@/lib/api/event-processing';

// connecting: subscribed, socket not yet acked. live: the socket is connected — the
// feed is flowing. reconnecting: the socket closed or the operation terminally ended
// and we are re-establishing it. (Same lifecycle as useAlarmStream.)
export type DetectionStreamStatus = 'connecting' | 'live' | 'reconnecting';

// Flat re-subscribe delay after a terminal end — a self-limiting retry loop, no tight
// spin against a hard-down server (mirrors useAlarmStream).
const RESUBSCRIBE_DELAY_MS = 3000;

// useDetectionStream taps the live DETECT detection feed (ADR-037) for one device profile
// and invokes onEvent for each firing (raised/resolved). onReconnect fires after the socket
// re-establishes following a drop, so the caller can re-run its ruleHealth query to refresh
// the durable counts that a stream gap may have advanced. It self-heals exactly like
// useAlarmStream: graphql-ws auto-retries a transient close; a terminal error or a
// server-side complete schedules a fresh subscription. Callbacks are held in refs so a new
// identity each render never tears down the live subscription. A blank profileToken makes it
// inert (no subscription) — the drawer/tab can mount it before a profile is selected.
export function useDetectionStream(
  profileToken: string,
  onEvent: (ev: DetectionEvent) => void,
  onReconnect?: () => void,
): { status: DetectionStreamStatus } {
  const [status, setStatus] = useState<DetectionStreamStatus>('connecting');
  // Bumping generation forces the effect to re-subscribe after a terminal end.
  const [generation, setGeneration] = useState(0);
  const onEventRef = useRef(onEvent);
  onEventRef.current = onEvent;
  const onReconnectRef = useRef(onReconnect);
  onReconnectRef.current = onReconnect;

  useEffect(() => {
    if (!profileToken) return;
    let resubscribeTimer: number | null = null;
    const scheduleResubscribe = () => {
      if (resubscribeTimer != null) return;
      resubscribeTimer = window.setTimeout(() => setGeneration((g) => g + 1), RESUBSCRIBE_DELAY_MS);
    };

    const sink: SubscriptionSink<DetectionStreamData> = {
      next: (data) => {
        // An event confirms the operation is live even when a re-subscription reused an
        // already-open socket (no fresh 'connected' fires in that case).
        setStatus('live');
        onEventRef.current(data.detectionStream);
      },
      connected: (wasRetry) => {
        setStatus('live');
        if (wasRetry) onReconnectRef.current?.();
      },
      closed: () => setStatus('reconnecting'),
      error: () => {
        setStatus('reconnecting');
        scheduleResubscribe();
      },
      complete: () => {
        setStatus('reconnecting');
        scheduleResubscribe();
      },
    };

    const unsubscribe = subscribe('event-processing', DETECTION_STREAM, { profileToken }, sink);
    return () => {
      if (resubscribeTimer != null) window.clearTimeout(resubscribeTimer);
      unsubscribe();
    };
  }, [generation, profileToken]);

  return { status };
}
