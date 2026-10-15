// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The lazy registration shared by the widget families that ship as their own chunk. A family's
// descriptors stay eager (the catalog, the editor and the validators read them), and each
// registration's component loads the family's chunk on first mount, together, from one fetch.
//
// Until the chunk arrives the widget renders nothing inside its box. A chunk that fails to load is
// THROWN, so the widget's error boundary shows the failure: an empty box that looks like a widget
// with nothing to say is the outcome this must never produce.
//
// The `import()` itself stays in each family's own module, as a literal: that is what makes the
// bundler split the chunk, and it is what the dist-shape guard looks for.

import { useEffect, useState, type ComponentType } from 'react';
import type { MeasurementStreamState, WidgetProps } from '@devicechain/widget-sdk';

export function lazyChunk<M extends Record<string, unknown>, D = MeasurementStreamState>(importer: () => Promise<M>) {
  let loaded: M | undefined;
  let loading: Promise<M> | undefined;

  // Memoized: every widget of the family on a board shares one fetch. Once it has resolved,
  // `loaded` lets a later mount render synchronously, with no empty first frame.
  const load = (): Promise<M> => {
    loading ??= importer().then((m) => (loaded = m));
    return loading;
  };

  // `P` is the data a given component takes when it differs from the family's default `D` (a chunk
  // that holds widgets of two channels).
  function component<P = D>(name: keyof M & string) {
    function LazyChunkWidget(props: WidgetProps<P>) {
      const [mod, setMod] = useState(loaded);
      const [error, setError] = useState<unknown>();
      useEffect(() => {
        if (!mod) load().then(setMod, setError);
      }, [mod]);
      if (error !== undefined) throw error;
      if (!mod) return null;
      const Component = mod[name] as ComponentType<WidgetProps<P>>;
      return <Component {...props} />;
    }
    LazyChunkWidget.displayName = `Lazy(${name})`;
    return LazyChunkWidget;
  }

  return { load, component };
}
