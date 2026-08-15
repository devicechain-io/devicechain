// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// One labelled number in a batch's fact grid. Extracted from the detail page's Summary so
// the cancellation report renders its four counts in the SAME visual unit the batch's own
// counts use — an operator reading "Stopped 900" directly under "Accepted 1000" is
// comparing them, and two hand-rolled label/value stacks are how those two rows would come
// to sit at different sizes.

import type { ReactNode } from 'react';

export function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div>
      <dt className="text-xs uppercase tracking-wider text-muted-foreground">{label}</dt>
      <dd className="mt-1 text-sm text-foreground">{children}</dd>
    </div>
  );
}
