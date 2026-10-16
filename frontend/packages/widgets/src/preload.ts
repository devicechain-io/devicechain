// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// preloadWidgets fetches the lazily loaded code of the given widget types, so a host that awaits
// it before rendering a board draws every widget on the first frame instead of an empty box
// while a chunk is in flight. A host that never calls it loses nothing: each widget still loads
// its own chunk on first mount.
//
// A type with no lazy code (an eager built-in, an extension, an unknown id) is skipped. A chunk
// that fails to load does not reject this: a failed load is not remembered, so the widget fetches
// its chunk again on mount, and if that fails too the error is thrown to its error boundary, which
// is where the failure belongs.

import type { WidgetRegistry } from '@devicechain/widget-sdk';

export async function preloadWidgets(registry: WidgetRegistry, types: Iterable<string>): Promise<void> {
  const loads: Promise<unknown>[] = [];
  for (const type of new Set(types)) {
    const preload = (registry.implementation(type)?.component as { preload?: unknown } | undefined)?.preload;
    if (typeof preload === 'function') loads.push((preload as () => Promise<unknown>)().catch(() => undefined));
  }
  await Promise.all(loads);
}
