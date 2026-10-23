// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The explained empty state, as the EAGER widgets (the latest-value card, the gauge, the line chart)
// reach it. The state itself (no-reading.tsx and the SDK's empty-state) loads as a chunk of its own on
// first use: a board whose widgets all have data never downloads it, and the first-load budget
// is not spent on words the common case does not show. Until it arrives the widget's box is empty,
// which is the same as the one frame an empty widget was blank for before there was a state to show.

import { useEffect, useState } from 'react';

import { lazyChunk } from './lazy-chunk';
import type { NoReadingProps } from '../no-reading';

export const NoReading = lazyChunk<typeof import('../no-reading')>(() => import('../no-reading')).component<never, NoReadingProps>('NoReading');

// True once `empty` has held for `ms`. A chart is blank for a moment on every board load while its
// history seed is read, so it explains the absence only after that moment, not in a flash before data.
export function useSettledEmpty(empty: boolean, ms = 1500): boolean {
  const [settled, setSettled] = useState(false);
  useEffect(() => {
    if (!empty) {
      setSettled(false);
      return;
    }
    const timer = setTimeout(() => setSettled(true), ms);
    return () => clearTimeout(timer);
  }, [empty, ms]);
  return empty && settled;
}

