// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The board itself: the real dashboard renderer over a recorded data source.
//
// This composes what apps/dashboard's viewer composes (slot cascade, candidate provider,
// selection overlay, renderer) with three deliberate differences:
//   - the data source is a RecordedDataSource, so there is no hub, no GraphQL client and
//     no session;
//   - NO `actions` seam is passed, so nothing can render an acknowledge, clear or send
//     control (the widgets draw those only when an actions seam exists);
//   - `seedHistory` is off: the source back-fills its own history from the recording, and
//     the backend must not be asked for it.
//
// The selection overlay lives HERE and not in the player, so it survives a seek: a seek
// swaps the data source (every widget re-subscribes and resets), and the viewer's machine
// pick stays where they put it.

import { effectiveBindings, type DashboardDefinition, type SlotBinding, type WidgetDataSource } from '@devicechain/dashboards';
import type { DeviceResolver, EntityCandidateLister, SelectionTarget } from '@devicechain/dashboards';
import {
  DashboardRenderer,
  MapRuntimeProvider,
  TenantBasemapProvider,
  useResolvedBindings,
  useSlotCandidates,
} from '@devicechain/widgets';
import { memo, useCallback, useMemo, useState } from 'react';

import { MAP_RUNTIME } from './map-runtime';

function BoardView({
  definition,
  source,
  resolver,
  lister,
  themeRoot,
}: {
  definition: DashboardDefinition;
  source: WidgetDataSource;
  resolver: DeviceResolver;
  lister: EntityCandidateLister;
  themeRoot: Element | undefined;
}) {
  const base = useMemo(() => effectiveBindings(definition), [definition]);
  const [selection, setSelection] = useState<Record<string, SlotBinding>>({});
  const select = useCallback((target: SelectionTarget) => {
    setSelection((prev) => ({ ...prev, [target.slot]: target.binding }));
  }, []);
  const bindings = useResolvedBindings(definition, base, selection, resolver);
  // The lister is what makes the entity selector offer machines; without candidates the
  // widget is inert. (Its "live dashboard" notice, which a replay must never print, is what
  // it shows when NO `select` callback is wired, so `select` below is load-bearing too.)
  const candidates = useSlotCandidates(definition, bindings, resolver, lister);

  return (
    <TenantBasemapProvider basemap={null}>
      <MapRuntimeProvider runtime={MAP_RUNTIME}>
        <DashboardRenderer
          definition={definition}
          hub={source}
          seedHistory={false}
          // Recorded instants print on the recording's own clock, not the viewer's.
          timeZone="UTC"
          themeRoot={themeRoot}
          bindings={bindings}
          select={select}
          candidates={candidates}
        />
      </MapRuntimeProvider>
    </TenantBasemapProvider>
  );
}

export const Board = memo(BoardView);
