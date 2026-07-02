// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useState } from 'react';

// useDebouncedValue returns a copy of `value` that only updates after it has
// stayed unchanged for `delayMs`. Use it to feed a fast-changing input (e.g. a
// text filter) into a query dependency so the query fires once the user pauses,
// not on every keystroke.
export function useDebouncedValue<T>(value: T, delayMs = 300): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const id = setTimeout(() => setDebounced(value), delayMs);
    return () => clearTimeout(id);
  }, [value, delayMs]);
  return debounced;
}
