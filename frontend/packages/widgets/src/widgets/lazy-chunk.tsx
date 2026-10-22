// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The lazy registration shared by the widget families that ship as their own chunk. A family's
// descriptors stay eager (the catalog, the editor and the validators read them), and each
// registration's component loads the family's chunk on first mount, together, from one fetch.
//
// Until the chunk arrives the widget renders nothing inside its box (or the `Fallback` its
// registration names: a feed uses ChunkLoadingFrame), unless the host awaited preloadWidgets
// (preload.ts) for the board first: then the chunk is already here and the first frame is complete. A chunk that fails to load is THROWN, so the widget's error boundary shows
// the failure: an empty box that looks like a widget with nothing to say is the outcome this must
// never produce. The failure is NOT remembered: the next mount (the boundary's "Reset widget")
// fetches the chunk again, so a blip on a wall display's network is not a widget that stays broken
// until the page is reloaded. React.lazy remembers it, which is why the feeds load through here too.
//
// The `import()` itself stays in each family's own module, as a literal: that is what makes the
// bundler split the chunk, and it is what the dist-shape guard looks for.

import { useEffect, useState, type ComponentType } from 'react';
import { optString, WidgetFrame, type MeasurementStreamState, type WidgetProps } from '@devicechain/widget-sdk';
import type { WidgetInstance } from '@devicechain/dashboards';

// What a feed chunk shows while it is in flight: the widget's frame and title, marked busy, rather
// than an empty box. A feed is chosen by the widget's OPTIONS (a chart set to one line per device),
// which preloadWidgets cannot see -- it is handed types -- so its first frame is often this one.
export function ChunkLoadingFrame({ widget }: { widget: WidgetInstance }) {
  return (
    <WidgetFrame title={optString(widget.options, 'title')}>
      <div data-chunk-loading="" aria-busy="true" style={{ width: '100%', height: '100%' }} />
    </WidgetFrame>
  );
}

export function lazyChunk<M extends Record<string, unknown>, D = MeasurementStreamState>(importer: () => Promise<M>) {
  let loaded: M | undefined;
  let loading: Promise<M> | undefined;

  // Memoized: every widget of the family on a board shares one fetch. Once it has resolved,
  // `loaded` lets a later mount render synchronously, with no empty first frame. A rejection is
  // forgotten, so the next load fetches again; only a mount asks, so a chunk that keeps failing
  // is never retried in a loop.
  const load = (): Promise<M> =>
    (loading ??= importer().then(
      (m) => (loaded = m),
      (err: unknown) => {
        loading = undefined;
        throw err;
      },
    ));

  // `P` is the data a given component takes when it differs from the family's default `D` (a chunk
  // that holds widgets of two channels). `Props` is for a component that is not a widget at all.
  // `alsoPreload` is code the component needs beyond the chunk that is itself loaded on demand (the
  // chart engine, for a chart in the chunk): preloadWidgets fetches both, in parallel.
  function component<P = D, Props extends object = WidgetProps<P>>(
    name: keyof M & string,
    alsoPreload?: () => Promise<unknown>,
    // Drawn until the chunk arrives (nothing, when absent).
    Fallback?: ComponentType<Props>,
  ) {
    function LazyChunkWidget(props: Props) {
      const [mod, setMod] = useState(loaded);
      const [error, setError] = useState<unknown>();
      useEffect(() => {
        if (!mod) load().then(setMod, setError);
      }, [mod]);
      if (error !== undefined) throw error;
      if (!mod) return Fallback ? <Fallback {...props} /> : null;
      const Component = mod[name] as ComponentType<Props>;
      return <Component {...props} />;
    }
    LazyChunkWidget.displayName = `Lazy(${name})`;
    // What preloadWidgets calls, so a host that awaits it renders this widget on its first frame.
    LazyChunkWidget.preload = alsoPreload ? () => Promise.all([load(), alsoPreload()]).then(([m]) => m) : load;
    return LazyChunkWidget;
  }

  return { load, component };
}
