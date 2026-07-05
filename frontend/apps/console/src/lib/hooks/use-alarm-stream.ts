// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useRef, useState } from 'react';
import { subscribe, type SubscriptionSink } from '@devicechain/client';
import { ALARM_STREAM, type AlarmEvent, type AlarmStreamData } from '@/lib/api/alarms';

// The narrowing filter mirrored onto the live feed. Kept to the facets the alarm
// list itself filters on (state, severity) so the stream carries mostly relevant
// events; acknowledged has no stream facet, and a non-matching event that slips
// through is harmless — it only triggers a reconcile.
export interface AlarmStreamFilter {
  state?: string;
  severity?: string;
}

// connecting: subscribe() called, no event or error yet. live: at least one event
// (or the socket is healthy) — the feed is flowing. error: the operation or socket
// failed; graphql-ws will retry, and a later event flips it back to live.
export type AlarmStreamStatus = 'connecting' | 'live' | 'error';

// useAlarmStream taps the live alarm-events feed (ADR-037) for the current tenant and
// invokes onEvent for each transition. Because delivery is best-effort (at-most-once,
// per the schema), onEvent is a reconcile trigger — not the row source of truth; the
// caller re-queries to reconcile. onEvent is held in a ref so a fresh callback
// identity each render doesn't tear down and rebuild the subscription — it
// re-subscribes only when the filter changes.
export function useAlarmStream(
  filter: AlarmStreamFilter,
  onEvent: (ev: AlarmEvent) => void,
): { status: AlarmStreamStatus } {
  const [status, setStatus] = useState<AlarmStreamStatus>('connecting');
  const onEventRef = useRef(onEvent);
  onEventRef.current = onEvent;

  const { state, severity } = filter;
  useEffect(() => {
    setStatus('connecting');
    const sink: SubscriptionSink<AlarmStreamData> = {
      next: (data) => {
        setStatus('live');
        onEventRef.current(data.alarmStream);
      },
      error: () => setStatus('error'),
    };
    const unsubscribe = subscribe(
      'device-management',
      ALARM_STREAM,
      {
        originatorType: null,
        originator: null,
        state: state ?? null,
        severity: severity ?? null,
        alarmKey: null,
      },
      sink,
    );
    return unsubscribe;
  }, [state, severity]);

  return { status };
}
