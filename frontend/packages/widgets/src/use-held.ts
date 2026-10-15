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
//
// A held snapshot that is ready while the live one is loading again means the stream was re-pointed:
// the alarm and location streams read as loading in the very render that carries a new subscription
// (their snapshots are tagged with it, so this holds even for a source that answers inside
// subscribe), or when the host replaces the hub itself (new authorities), which re-subscribes the same
// way -- and at no other time: a dropped socket, a failed query and the reconnect's re-query all
// answer the SAME subscription, ready, and leave the hold alone. Selection stays live while a board
// is paused. That hold belongs to the previous entity, so it is dropped and the new entity's first
// ready snapshot is held instead. Kept, the alarm rows and map markers of the device the board was
// paused on would stay under the new device's name. So the hold follows the live value whenever the
// two disagree on being ready.
export function useHeld<T>(value: T, hold: boolean, ready: (v: T) => boolean): T {
  const [held, setHeld] = useState<{ value: T } | undefined>(undefined);
  if (!hold) {
    if (held !== undefined) setHeld(undefined);
    return value;
  }
  if (held === undefined || ready(held.value) !== ready(value)) {
    setHeld({ value });
    return value;
  }
  return held.value;
}
