// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { ReactNode } from 'react';
import type { Area } from '@devicechain/client';
import { Navigate, matchPath, useLocation } from 'react-router-dom';
import { AreaGate } from '@/components/AreaUnavailable';
import { ResourceListPage, ResourceDetailPage } from '@/components/registry';
import { REGISTRY_RESOURCES } from '@/routes/resources';

// Resolves a path against the registry families and renders its generic list or detail page.
// It is the one lazily loaded module that imports the resource definitions, so those (and their
// forms and GraphQL documents) are not part of the first download. A path that names no family
// goes home, as the app's catch-all does.
export default function RegistryRoute() {
  const { pathname } = useLocation();
  for (const r of REGISTRY_RESOURCES) {
    // Keyed by the resource's base path so React remounts on a switch between families:
    // otherwise the previous resource's data bleeds through the reused instance.
    if (matchPath({ path: r.basePath, end: true }, pathname)) {
      return gated(r.area, <ResourceListPage key={r.basePath} resource={r} />);
    }
    if (matchPath({ path: `${r.basePath}/:token`, end: true }, pathname)) {
      return gated(r.area, <ResourceDetailPage key={r.basePath} resource={r} />);
    }
  }
  return <Navigate to="/" replace />;
}

function gated(area: Area | undefined, page: ReactNode) {
  return area ? <AreaGate area={area}>{page}</AreaGate> : page;
}
