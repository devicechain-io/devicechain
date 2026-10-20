// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { ReactNode } from 'react';
import type { Area } from '@devicechain/client';
import { Navigate, Route, Routes } from 'react-router-dom';
import { AreaGate } from '@/components/AreaUnavailable';
import { ResourceListPage, ResourceDetailPage } from '@/components/registry';
import { REGISTRY_RESOURCES } from '@/routes/resources';

// The registry families' routes: every family's list and detail path renders the one generic
// page for that family. It is the one lazily loaded module that imports the resource
// definitions, so those (and their forms and GraphQL documents) are not part of the first
// download. App.tsx mounts it under a splat, and these are DESCENDANT routes rather than a
// hand-rolled match over the path, so React Router resolves them exactly as it did when they sat
// in App.tsx's table: the same ranking, and a detail page's `:token` reaches useParams() (a
// matchPath here would leave the page with only the splat param, and every detail page would
// load the empty token). A path that names no family goes home, as the app's catch-all does.
export default function RegistryRoute() {
  return (
    <Routes>
      {/* Every registry list/detail renders through the one generic page component, so React
          reuses the instance across routes. Key each element by its resource's base path to
          force a fresh mount on switch: otherwise the previous resource's data bleeds through. */}
      {REGISTRY_RESOURCES.flatMap((r) => [
        <Route
          key={`${r.basePath}-list`}
          path={r.basePath.slice(1)}
          element={gated(r.area, <ResourceListPage key={r.basePath} resource={r} />)}
        />,
        <Route
          key={`${r.basePath}-detail`}
          path={`${r.basePath.slice(1)}/:token`}
          element={gated(r.area, <ResourceDetailPage key={r.basePath} resource={r} />)}
        />,
      ])}
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  );
}

// gated wraps a registry page in its area's gate when the resource names an area a profile
// can leave out.
function gated(area: Area | undefined, page: ReactNode) {
  return area ? <AreaGate area={area}>{page}</AreaGate> : page;
}
