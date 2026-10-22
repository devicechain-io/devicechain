// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The data feed of a presence widget (the device status card, the online doughnut): the roster of
// the datasource, then device-state's connectivity for it on a poll. Loaded on first use
// (connected-widget.tsx imports it dynamically), so a board with no presence widget never downloads it.
//
// It hands the widget the SAME `data.members` a fleet widget reads, with each member's `presence`
// filled and no readings -- connectivity folded into the measurement channel's member feed rather than
// a channel of its own, so the exported channel union does not move.
//
//   - ONE subscription per widget (hub.subscribePresence), one batch read per poll however many members;
//   - a member the snapshot does not carry has no state row (it has never sent anything): `unknown`,
//     a settled answer, never `pending`;
//   - a read that fails before any snapshot has landed leaves every member `failed`; once one has, a
//     later blip keeps the last good answer (the poll heals it), as the alarm and location channels do;
//   - a paused board holds the snapshot on screen (useHeld) rather than moving under a "Paused" badge;
//   - the frame's freshness is the age of the STATUS -- when device-state was last read successfully --
//     not the devices' last activity. What these widgets show is the platform's verdict (online or
//     offline), and that verdict is current as long as it keeps being read: an offline device is silent
//     by definition, and an asserted device can be connected and quiet, so greying a card out for the
//     device's silence would strip the one colour an outage takes, or grey out a true "Online". A read
//     that keeps failing is what makes the answer old, and that is what goes stale. "Last seen" (the
//     device's last activity) is the widget's own caption.

import {
  type DevicePresence,
  type FleetMember,
  type FleetState,
  type MeasurementStreamState,
  type MemberRoster,
  type WidgetActions,
  type WidgetDataSource,
  type WidgetDescriptor,
  type WidgetInstance,
} from '@devicechain/dashboards';
import { resolveStaleAfterSeconds, useWidgetTimeRange, WidgetFreshnessProvider } from '@devicechain/widget-sdk';
import { useEffect, useMemo, useState, type ComponentType } from 'react';

import { useHeld } from './use-held';

export interface PresenceFeedProps {
  hub: WidgetDataSource;
  widget: WidgetInstance;
  descriptor: WidgetDescriptor;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  Component: ComponentType<any>;
  actions: WidgetActions | undefined;
  ErrorFrame: ComponentType<{ widget: WidgetInstance; error: unknown }>;
}

interface Store {
  // The selector this store was filled for: a render carrying a new selector must not show the
  // previous one's members under it while the effect that resets the store has yet to run.
  key: string | null;
  roster: MemberRoster | null;
  states: Map<string, DevicePresence> | null;
  forbidden: boolean;
  error: unknown | null;
  // When the last snapshot landed (epoch ms): the age of the status on screen.
  readAt: number | null;
}

const emptyStore = (key: string | null): Store => ({ key, roster: null, states: null, forbidden: false, error: null, readAt: null });

// The state a presence widget renders from, built from the store. Exported for the tests.
export function buildPresenceFleet(store: Pick<Store, 'roster' | 'states' | 'forbidden' | 'error'>): FleetState {
  if (store.forbidden) return { status: 'forbidden', members: [], total: 0, truncated: false };
  if (!store.roster) return { status: 'loading', members: [], total: 0, truncated: false };
  if (store.roster.unbound) return { status: 'unbound', members: [], total: 0, truncated: false };
  const { states, error } = store;
  const members = store.roster.members.map((ref): FleetMember => {
    const state = states?.get(ref.token);
    return {
      token: ref.token,
      name: ref.name,
      latest: {},
      seed: 'ready',
      observed: [],
      presence: state
        ? { status: 'ready', active: state.active, lastActivityTime: state.lastActivityTime, inactivityTimeout: state.inactivityTimeout }
        : states
          ? { status: 'unknown' }
          : error
            ? { status: 'failed' }
            : { status: 'pending' },
    };
  });
  return { status: 'ready', members, total: store.roster.total, truncated: store.roster.total > members.length };
}

export default function PresenceFeed({ hub, widget, descriptor, Component, actions }: PresenceFeedProps) {
  const range = useWidgetTimeRange();
  const ds = widget.datasource;
  const dsKey = JSON.stringify(ds ?? null);
  const supported = hub.subscribePresence !== undefined;
  const [store, setStore] = useState<Store>(() => emptyStore(dsKey));

  useEffect(() => {
    setStore(emptyStore(dsKey));
    if (!ds || !supported) return;
    return hub.subscribePresence!(ds, {
      roster: (roster) => setStore((s) => ({ ...s, roster })),
      next: (states) => setStore((s) => ({ ...s, states: new Map(states.map((p) => [p.deviceToken, p])), error: null, readAt: Date.now() })),
      forbidden: () => setStore((s) => ({ ...s, forbidden: true })),
      error: (error) => setStore((s) => ({ ...s, error })),
    });
    // `ds` is read through `dsKey` (value identity), not by reference.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hub, dsKey, supported]);

  const live = useMemo<{ fleet: FleetState; readAt: number | null }>(() => {
    if (!supported) return { fleet: { status: 'unsupported', members: [], total: 0, truncated: false }, readAt: null };
    const current = store.key === dsKey ? store : emptyStore(dsKey);
    return { fleet: buildPresenceFleet(current), readAt: current.readAt };
  }, [store, dsKey, supported]);
  // Held together, so a paused board's frame ages from the snapshot it is showing.
  const held = useHeld(live, range?.paused === true, (s) => s.fleet.status !== 'loading');
  const members = held.fleet;

  const freshness = useMemo(
    () =>
      descriptor.freshness === true
        ? { at: held.readAt ?? undefined, staleAfterSeconds: resolveStaleAfterSeconds(widget.options) }
        : undefined,
    [descriptor, widget.options, held.readAt],
  );
  const data: MeasurementStreamState = { latest: {}, samples: [], error: null, members };
  return (
    <WidgetFreshnessProvider freshness={freshness}>
      <Component widget={widget} data={data} actions={actions} />
    </WidgetFreshnessProvider>
  );
}
