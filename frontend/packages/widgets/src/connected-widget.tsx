// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// ConnectedWidget is the one place the imperative DashboardHub meets the pure widget
// contract. It looks the widget up in the host's REGISTRY and dispatches on the registered
// descriptor's data CHANNEL: a measurement widget subscribes its datasource (windowed per its
// binding policy); an alarm widget subscribes its scope+filters through the hub's alarm
// channel; and so on. Either way it resolves the data and renders the registered component
// with { widget, data }. A dashboard renderer maps over its widgets rendering one
// ConnectedWidget each.
//
// Each widget renders inside its own containment (widget-containment.tsx): a throw shows a
// local diagnostic with a reset control and the siblings keep rendering. A widget the
// registry cannot resolve (an unknown or unavailable package, a version it cannot read) is a
// placeholder with a specific reason: it reads nothing — no channel, no availability query —
// and is handed no actions.

import {
  UNKNOWN_WIDGET_TYPE,
  type MeasurementSample,
  type UnresolvedWidgetReason,
  type WidgetActions,
  type WidgetDataSource,
  type WidgetDescriptor,
  type WidgetInstance,
} from '@devicechain/dashboards';
import {
  newestSampleTime,
  resolveStaleAfterSeconds,
  WidgetCandidatesProvider,
  WidgetFreshnessProvider,
  WidgetSelectProvider,
  useWidgetTimeRange,
  type WidgetFreshness,
  type WidgetRegistry,
} from '@devicechain/widget-sdk';
import { lazy, Suspense, useMemo, type ComponentType, type ReactNode } from 'react';

import { alarmSubscription, commandSubscription, measurementSpan, measurementWindow, membersMode } from './binding-policy';
import { useHeld } from './use-held';
import { defaultWidgetRegistry } from './default-registry';
import {
  optString,
  useAlarmStream,
  useCommandStream,
  useDatasourceAvailability,
  useLocationStream,
  useMeasurementStream,
  useWidgetCandidates,
  useWidgetSelect,
  WidgetFrame,
} from '@devicechain/widget-sdk';
import { WidgetContainment, useReportRejection } from './widget-containment';
import { narrowActions, narrowCandidates, narrowSelect } from './widget-host';

export interface ConnectedWidgetProps {
  widget: WidgetInstance;
  hub: WidgetDataSource;
  // The registry that resolves the widget's component, channel and binding policy. Omitted =
  // the built-in registry. A host composing extension packages passes the SAME registry it
  // parsed the definition with.
  registry?: WidgetRegistry;
  // The action seam (writes). Narrowed per widget to the interactions its descriptor
  // declares; undefined = read-only.
  actions?: WidgetActions;
  // Optional history backfill for this widget (from bucketedMeasurements), seeded
  // under the live tail so a chart isn't blank until the first live sample. Ignored by
  // every channel but measurement (the others reconcile from a query, not a history seed).
  initialSamples?: MeasurementSample[];
}

export function ConnectedWidget(props: ConnectedWidgetProps) {
  const { widget } = props;
  const registry = props.registry ?? defaultWidgetRegistry();
  // A change to what the widget is configured with retries a widget that threw on the old one.
  const resetKey = JSON.stringify([widget.type, widget.options ?? null, widget.datasource ?? null]);
  return (
    <WidgetContainment title={optString(widget.options, 'title')} resetKey={resetKey}>
      <ConnectedWidgetBody {...props} registry={registry} />
    </WidgetContainment>
  );
}

type BodyProps = ConnectedWidgetProps & { registry: WidgetRegistry };

// The component a registration renders. Its props are erased in the registry (one registry
// holds components of every channel); the channel adapters below supply the matching data.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
type AnyWidgetComponent = ComponentType<any>;

function ConnectedWidgetBody({ widget, hub, registry, actions, initialSamples }: BodyProps) {
  const unresolved = widget.type === UNKNOWN_WIDGET_TYPE;
  const descriptor = unresolved ? undefined : registry.descriptor(widget.type);
  const implementation = unresolved ? undefined : registry.implementation(widget.type);

  // A widget bound to a since-deleted device (its stable token no longer resolves)
  // shows an "unavailable" pane rather than a blank one — checked optimistically, so a
  // valid widget renders from its stream without waiting on this. An unresolved widget asks
  // nothing: it is never given a datasource to check. Nor does a widget whose descriptor
  // binds none (label, image, the selector): a datasource left on its stored JSON is one it
  // never reads, so it must not turn the widget into an "unavailable" pane.
  const readsDatasource = descriptor !== undefined && descriptor.datasource.mode !== 'none';
  const availability = useDatasourceAvailability(hub, readsDatasource ? widget.datasource : undefined);
  if (availability === 'unavailable') {
    return <WidgetUnavailableFrame widget={widget} />;
  }

  if (widget.type === UNKNOWN_WIDGET_TYPE) {
    return <UnresolvedWidgetFrame storedType={widget.raw.type} reason={widget.reason} />;
  }
  if (!descriptor || !implementation) {
    // Parsed against a catalog this registry does not hold: say so rather than render nothing.
    return (
      <UnresolvedWidgetFrame
        storedType={widget.type}
        reason={{ code: 'unregistered', message: `"${widget.type}" is not registered in this host's widget registry` }}
      />
    );
  }

  return (
    <HostScope descriptor={descriptor} actions={actions}>
      {(hostActions) => (
        <ChannelBody
          widget={widget}
          hub={hub}
          descriptor={descriptor}
          Component={implementation.component}
          actions={hostActions}
          initialSamples={initialSamples}
        />
      )}
    </HostScope>
  );
}

// HostScope hands ONE widget only the interactions it declared: a narrowed action seam, and
// the ambient selection / candidate providers re-provided so an undeclared capability
// reads as "not wired" to a widget that asks.
function HostScope({
  descriptor,
  actions,
  children,
}: {
  descriptor: WidgetDescriptor;
  actions: WidgetActions | undefined;
  children: (actions: WidgetActions | undefined) => ReactNode;
}) {
  const report = useReportRejection();
  const select = useWidgetSelect();
  const candidates = useWidgetCandidates();
  const narrowed = useMemo(() => narrowActions(actions, descriptor, report), [actions, descriptor, report]);
  const narrowedSelect = useMemo(() => narrowSelect(select, descriptor), [select, descriptor]);
  const narrowedCandidates = useMemo(
    () => narrowCandidates(candidates, descriptor, report),
    [candidates, descriptor, report],
  );
  return (
    <WidgetSelectProvider select={narrowedSelect}>
      <WidgetCandidatesProvider candidates={narrowedCandidates}>{children(narrowed)}</WidgetCandidatesProvider>
    </WidgetSelectProvider>
  );
}

interface ChannelProps {
  widget: WidgetInstance;
  hub: WidgetDataSource;
  descriptor: WidgetDescriptor;
  Component: AnyWidgetComponent;
  actions: WidgetActions | undefined;
  initialSamples?: MeasurementSample[];
}

// The channel is the only thing the runtime branches on — never the widget id.
function ChannelBody(props: ChannelProps) {
  switch (props.descriptor.channel) {
    case 'alarm':
      return <AlarmConnectedWidget {...props} />;
    case 'control':
      return <ControlConnectedWidget {...props} />;
    case 'location':
      return <LocationConnectedWidget {...props} />;
    case 'aggregate':
      return <AggregateConnectedWidget {...props} />;
    case 'selection':
    case 'none':
      return <StaticConnectedWidget {...props} />;
    case 'measurement':
      return <MeasurementConnectedWidget {...props} />;
  }
}

// A selection widget (entity-selector) consumes no hub data — it reads the ambient candidate
// provider + selection callback; a `none` widget is static. Neither subscribes anything.
function StaticConnectedWidget({ widget, Component, actions }: ChannelProps) {
  return <Component widget={widget} data={null} actions={actions} />;
}

// The fleet feed is its own chunk (fleet-feed.tsx), like the aggregate feed: a board without a fleet
// table or a per-device bar chart never downloads it. A chunk that fails to load is thrown to the
// widget's containment, which shows the failure.
const FleetFeed = lazy(() => import('./fleet-feed'));

function MeasurementConnectedWidget(props: ChannelProps) {
  // Read member by member, or as one stream keyed by measurement name: decided by the descriptor
  // and the widget's options, never by the widget id.
  if (membersMode(props.descriptor, props.widget)) {
    const { widget, hub, descriptor, Component, actions } = props;
    return (
      <Suspense fallback={null}>
        <FleetFeed hub={hub} widget={widget} descriptor={descriptor} Component={Component} actions={actions} ErrorFrame={WidgetErrorFrame} />
      </Suspense>
    );
  }
  return <StreamConnectedWidget {...props} />;
}

function StreamConnectedWidget({ widget, hub, descriptor, Component, actions, initialSamples }: ChannelProps) {
  // The board's time range (D-1). A widget that declares `binding.windowSeconds` draws a SPAN: its
  // own pinned one, else the board's. Every measurement widget honors the pause, because a frozen
  // board must freeze its cards and gauges too -- a number that kept moving under a "Paused"
  // badge would be the worst possible answer.
  const range = useWidgetTimeRange();
  // The source's own clock (a recorded run is dated by the run), else the wall clock.
  const now = useMemo(() => (hub.now ? () => hub.now!() : Date.now), [hub]);
  const data = useMeasurementStream(hub, widget.datasource, {
    window: measurementWindow(descriptor, widget),
    windowSeconds: measurementSpan(descriptor, widget, range?.spanSeconds),
    pausedAt: range?.paused ? range.pausedAt : undefined,
    now,
    initialSamples,
  });

  // Opt-in through the descriptor. The age is of the newest sample among the measurements this
  // widget shows (its pinned one, else its datasource's, else whatever has arrived) -- right for
  // a chart or a table, which time every point or row. A single-value widget narrows it to the
  // sample it actually shows by passing that sample's time to its WidgetFrame (`sampleTime`).
  // The threshold is the widget's own option, and absent means age only.
  const freshness = useMemo<WidgetFreshness | undefined>(() => {
    if (descriptor.freshness !== true) return undefined;
    const pinned = optString(widget.options, 'measurement');
    const names = pinned ? [pinned] : widget.datasource?.measurements;
    return {
      at: newestSampleTime(data.latest, names),
      staleAfterSeconds: resolveStaleAfterSeconds(widget.options),
    };
  }, [descriptor, widget.options, widget.datasource, data.latest]);

  if (data.error) return <WidgetErrorFrame widget={widget} error={data.error} />;
  return (
    <WidgetFreshnessProvider freshness={freshness}>
      <Component widget={widget} data={data} actions={actions} />
    </WidgetFreshnessProvider>
  );
}

// The aggregate feed is its own chunk (aggregate-feed.tsx): a board without an aggregate widget
// never downloads it. Nothing renders until it arrives, and a chunk that fails to load is thrown to
// the widget's containment, which shows the failure.
const AggregateFeed = lazy(() => import('./aggregate-feed'));

function AggregateConnectedWidget({ widget, hub, descriptor, Component, actions }: ChannelProps) {
  return (
    <Suspense fallback={null}>
      <AggregateFeed
        hub={hub}
        widget={widget}
        descriptor={descriptor}
        Component={Component}
        actions={actions}
        ErrorFrame={WidgetErrorFrame}
      />
    </Suspense>
  );
}

function AlarmConnectedWidget({ widget, hub, descriptor, Component, actions }: ChannelProps) {
  // Frozen while the board is paused: the snapshot on screen at the pause (or the first one to land,
  // when the board opened paused) stays, and the live one is shown again on resume.
  const range = useWidgetTimeRange();
  const live = useAlarmStream(hub, alarmSubscription(descriptor, widget));
  const data = useHeld(live, range?.paused === true, (s) => !s.loading);

  // Show the error pane ONLY when there's nothing to display (the initial load failed).
  // Once a snapshot has loaded, a later transient poll/query error keeps the last-good
  // rows on screen — the channel re-queries every 30s and self-heals, so blanking a
  // populated table on one blip is the exact blink the console page avoids.
  if (data.error && data.alarms.length === 0 && data.total === 0) {
    return <WidgetErrorFrame widget={widget} error={data.error} />;
  }
  return <Component widget={widget} data={data} actions={actions} />;
}

function ControlConnectedWidget({ widget, hub, descriptor, Component, actions }: ChannelProps) {
  const data = useCommandStream(hub, commandSubscription(descriptor, widget));

  // Unlike the read-only widgets, a command widget is NEVER replaced by the error pane:
  // its primary control is the send form (a separate authority from reading history), so
  // a history poll error must not tear the form down. The widget surfaces the error
  // inline instead.
  return <Component widget={widget} data={data} actions={actions} />;
}

function LocationConnectedWidget({ widget, hub, Component, actions }: ChannelProps) {
  // The tile source lives in the widget's options, not here — it is a rendering choice, not
  // part of what the widget asks the platform for.
  const range = useWidgetTimeRange();
  const live = useLocationStream(hub, { datasource: widget.datasource });
  const data = useHeld(live, range?.paused === true, (s) => !s.loading);

  // Same rule as the alarm channel: show the error pane ONLY when there is nothing to
  // display. Once a snapshot has loaded, a later transient poll error keeps the last
  // known positions on screen — the channel re-polls and self-heals, and blanking a
  // populated map on one blip is worse than a marker being a few seconds stale.
  //
  // 🔴 A REFUSAL IS NOT AN ERROR AND MUST NOT REACH THIS BRANCH. The hub delivers it as
  // a snapshot state with `error` null, so the widget renders its own permission copy —
  // if it were surfaced as an error, every ordinary member would see "Data unavailable"
  // where the platform is working exactly as designed.
  if (data.error && data.locations.length === 0) {
    return <WidgetErrorFrame widget={widget} error={data.error} />;
  }
  return <Component widget={widget} data={data} actions={actions} />;
}

// The placeholder for a widget this build cannot resolve. It names the stored type and says
// WHY (not installed, no version metadata, a settings or SDK generation it cannot read); the
// stored object is otherwise untouched (the serializer writes it back verbatim).
function UnresolvedWidgetFrame({
  storedType: stored,
  reason,
}: {
  storedType: unknown;
  reason: UnresolvedWidgetReason | undefined;
}): ReactNode {
  const storedType = typeof stored === 'string' ? stored : '';
  const heading =
    (reason?.code ?? 'unregistered') === 'unregistered'
      ? 'This widget needs a newer viewer'
      : 'This widget cannot be shown here';
  return (
    <WidgetFrame>
      <div
        style={{
          display: 'flex',
          flexDirection: 'column',
          alignItems: 'center',
          justifyContent: 'center',
          gap: 4,
          width: '100%',
          height: '100%',
          padding: 12,
          boxSizing: 'border-box',
          textAlign: 'center',
          color: 'hsl(var(--muted-foreground))',
        }}
      >
        <div style={{ fontSize: 13, fontWeight: 600 }}>{heading}</div>
        {storedType && !reason?.message.includes(storedType) && (
          <div style={{ fontSize: 11, opacity: 0.8 }}>Widget type: {storedType}</div>
        )}
        {reason && <div style={{ fontSize: 11, opacity: 0.8, wordBreak: 'break-word' }}>{reason.message}</div>}
      </div>
    </WidgetFrame>
  );
}

// WidgetUnavailableFrame is shown when a widget's bound device no longer exists (a
// deleted device's stable token). Distinct from the error frame: not a transient
// failure to retry, but a settled "this entity is gone" — so it reads as a state, not
// an error.
function WidgetUnavailableFrame({ widget }: { widget: WidgetInstance }): ReactNode {
  return (
    <WidgetFrame title={optString(widget.options, 'title')}>
      <div
        style={{
          display: 'flex',
          flexDirection: 'column',
          alignItems: 'center',
          justifyContent: 'center',
          gap: 4,
          width: '100%',
          height: '100%',
          padding: 12,
          boxSizing: 'border-box',
          textAlign: 'center',
          color: 'hsl(var(--muted-foreground))',
        }}
      >
        <div style={{ fontSize: 20, lineHeight: 1 }} aria-hidden="true">⊘</div>
        <div style={{ fontSize: 13, fontWeight: 600 }}>Device unavailable</div>
        <div style={{ fontSize: 11, opacity: 0.8 }}>The bound device no longer exists.</div>
      </div>
    </WidgetFrame>
  );
}

// A resolution/socket error surfaces as a muted error state rather than a blank pane.
// Shared by both the measurement and alarm channels.
function WidgetErrorFrame({ widget, error }: { widget: WidgetInstance; error: unknown }): ReactNode {
  return (
    <WidgetFrame title={optString(widget.options, 'title')}>
      <div
        style={{
          display: 'flex',
          flexDirection: 'column',
          alignItems: 'center',
          justifyContent: 'center',
          gap: 4,
          width: '100%',
          height: '100%',
          padding: 12,
          boxSizing: 'border-box',
          textAlign: 'center',
          color: 'hsl(var(--muted-foreground))',
        }}
      >
        <div style={{ fontSize: 20, lineHeight: 1 }}>⚠</div>
        <div style={{ fontSize: 13, fontWeight: 600 }}>Data unavailable</div>
        <div style={{ fontSize: 11, opacity: 0.8, wordBreak: 'break-word' }}>{String(error)}</div>
      </div>
    </WidgetFrame>
  );
}
