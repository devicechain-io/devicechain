// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import {
  tokenColumn,
  nameColumn,
  createdColumn,
  type RegistryResource,
} from '@/components/registry';
import { listGeoFences, getGeoFence, deleteGeoFence, type GeoFence } from '@/lib/api/geofences';
import { GeoFenceForm } from './GeoFenceForm';
import { FenceHistoryPanel } from './FenceHistoryPanel';
import { fromGeometryDocument } from './geometry';

/** The vertex count an operator drew — the closing position is not one of them. */
function vertexCount(fence: GeoFence): number | null {
  const vertices = fromGeometryDocument(fence.geometry);
  return vertices ? vertices.length : null;
}

export const geofenceResource: RegistryResource<GeoFence> = {
  basePath: '/geofences',
  i18nKey: 'geofence',
  banner: 'areas',
  list: listGeoFences,
  load: getGeoFence,
  remove: deleteGeoFence,
  idOf: (f) => f.id,
  tokenOf: (f) => f.token,
  nameOf: (f) => f.name,
  columns: [
    tokenColumn<GeoFence>(),
    nameColumn<GeoFence>(),
    {
      header: 'entities:geofenceColVertices',
      cell: (f) => {
        const count = vertexCount(f);
        // An em dash rather than 0: a geometry this console cannot read is not a
        // fence with no corners, and showing 0 would invite someone to "fix" it
        // by redrawing over a shape that is perfectly valid to the engine.
        return count === null ? (
          <span className="text-muted-foreground">—</span>
        ) : (
          <span className="tabular-nums">{count}</span>
        );
      },
    },
    createdColumn<GeoFence>(),
  ],
  // 🔴 Keyed on the entity. The form seeds its ring and closed-state from the
  // entity ONCE, so a page that swapped in a different fence under a mounted form
  // would leave the previous fence's geometry in state — and updateGeoFence is a
  // full replace, so the next save would write it onto the new token. No route in
  // the console does that today; the key means none can start to.
  renderForm: (fence, onDone) => (
    <GeoFenceForm key={fence?.id ?? 'new'} entity={fence} onDone={onDone} />
  ),
  detailTabs: [
    {
      value: 'history',
      label: 'entities:geofenceHistoryTab',
      // Keyed like the form: the panel holds a chosen version in state, and a
      // different fence must not inherit it.
      render: (fence) => <FenceHistoryPanel key={fence.id} entity={fence} />,
    },
  ],
};
