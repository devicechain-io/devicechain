// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The explained empty state as the level and fleet widgets reach it; the single-device decision
// itself is no-reading-core.tsx.

import { WidgetEmptyState } from '@devicechain/widget-sdk/empty-state';

import type { LevelReading } from './level-reading';
import { LevelEmpty } from './level-readout';
import { NoReading } from './no-reading-core';

export { ASK_DELAY_MS, SETTLE_MS, emptyKindFor, lastReadingHint, NoReading, type NoReadingProps } from './no-reading-core';

// A whole-widget state the platform or the binding decides (a refusal, an unchosen device or group),
// in the shared look. The widget's own sentence stays as the explanation, so a fleet widget still
// says "Choose a device group", not the single-device wording.
export function Explained({ kind, message }: { kind: 'unbound' | 'forbidden'; message: string }) {
  return <WidgetEmptyState kind={kind} detail={message} />;
}

// The empty state of a level widget: its own configuration problem when it has one (the author's fix
// comes first), else the explained absence of a reading.
export function LevelEmptyFor({ reading, reason }: { reading: LevelReading; reason: string | undefined }) {
  if (reason !== undefined) return <LevelEmpty message={reason} />;
  return <NoReading unbound={reading.unbound} measurement={reading.name} lastSample={reading.sample} unit={reading.unit} precision={reading.precision} />;
}
