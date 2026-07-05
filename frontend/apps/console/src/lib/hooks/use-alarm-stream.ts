// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useRef, useState } from 'react';
import { subscribe, type SubscriptionSink } from '@devicechain/client';
import { ALARM_STREAM, type AlarmEvent, type AlarmStreamData } from '@/lib/api/alarms';

// connecting: subscribed, socket not yet acked. live: the socket is connected — the
// feed is flowing (whether or not events are arriving). reconnecting: the socket
// closed, or the operation terminally ended and we are re-establishing it.
export type AlarmStreamStatus = 'connecting' | 'live' | 'reconnecting';

// How long to wait before re-establishing a subscription that terminally ended
// (graphql-ws retries exhausted, a fatal close code, or a server-side complete).
// A flat delay is a self-limiting retry loop — no tight spin against a hard-down
// server — while the 30s query poll keeps the list fresh in the meantime.
const RESUBSCRIBE_DELAY_MS = 3000;

// useAlarmStream taps the live alarm-events feed (ADR-037) for the current tenant and
// invokes onEvent for each transition. It subscribes UNFILTERED: the events are pure
// reconcile triggers (never spliced into rows), and narrowing the server feed would
// suppress exactly the exit transitions — a CLEARED under a state=ACTIVE view — that
// should remove a row, silently stranding it. onReconnect fires after the socket
// re-establishes following a drop, so the caller can re-run its query to catch any
// transition missed during the gap (the schema's "re-run on reconnect" guidance).
//
// The feed self-heals: graphql-ws auto-retries a transient socket close; a terminal
// error or a server-side complete (both permanent for that operation) schedules a
// fresh subscription. Callbacks are held in refs so a new identity each render never
// tears down the live subscription.
export function useAlarmStream(
  onEvent: (ev: AlarmEvent) => void,
  onReconnect?: () => void,
): { status: AlarmStreamStatus } {
  const [status, setStatus] = useState<AlarmStreamStatus>('connecting');
  // Bumping generation forces the effect to re-subscribe after a terminal end.
  const [generation, setGeneration] = useState(0);
  const onEventRef = useRef(onEvent);
  onEventRef.current = onEvent;
  const onReconnectRef = useRef(onReconnect);
  onReconnectRef.current = onReconnect;

  useEffect(() => {
    let resubscribeTimer: number | null = null;
    const scheduleResubscribe = () => {
      if (resubscribeTimer != null) return;
      resubscribeTimer = window.setTimeout(() => setGeneration((g) => g + 1), RESUBSCRIBE_DELAY_MS);
    };

    const sink: SubscriptionSink<AlarmStreamData> = {
      next: (data) => {
        // An event confirms the operation is live even when a re-subscription
        // reused an already-open socket (no fresh 'connected' fires in that case).
        setStatus('live');
        onEventRef.current(data.alarmStream);
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

    const unsubscribe = subscribe(
      'device-management',
      ALARM_STREAM,
      { originatorType: null, originator: null, state: null, severity: null, alarmKey: null },
      sink,
    );
    return () => {
      if (resubscribeTimer != null) window.clearTimeout(resubscribeTimer);
      unsubscribe();
    };
  }, [generation]);

  return { status };
}
