// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';

// useHeld returns `value` live, and while `hold` is true the value it had when the hold began:
// the whole-snapshot channels (alarms, locations) deliver a complete state each time, so freezing
// one is just not looking at the newer ones. Nothing is lost -- the source keeps updating, and
// releasing the hold shows its current state at once.
//
// `ready` says whether a value is worth freezing on. A board that OPENS paused (a deep link) holds
// its first value, which is the "loading" one; freezing on that would leave the widget loading for
// as long as the pause lasts. So the hold takes the first value that is ready, and keeps it.
export function useHeld<T>(value: T, hold: boolean, ready: (v: T) => boolean): T {
  const [held, setHeld] = useState<{ value: T } | undefined>(undefined);
  if (!hold) {
    if (held !== undefined) setHeld(undefined);
    return value;
  }
  if (held === undefined || (!ready(held.value) && ready(value))) {
    setHeld({ value });
    return value;
  }
  return held.value;
}
