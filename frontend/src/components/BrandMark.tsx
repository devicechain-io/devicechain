// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { cn } from '@/lib/utils';

// The DeviceChain brandmark — a nested isometric cube in the brand blues
// (top #9aceec, right #208cb7, left #15678b) with white edges. This is a
// faithful hand-authored stand-in for the logo's hex-cube; swap in the
// Illustrator icon-only export when it lands. Multi-color by design, so it is
// not tied to `currentColor`; size it via `className` (e.g. "size-8").
export function BrandMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 64 64" fill="none" className={cn('size-6', className)} aria-hidden="true">
      <g stroke="#ffffff" strokeWidth={2.5} strokeLinejoin="round" strokeLinecap="round">
        <polygon points="32,10 52,21 32,32 12,21" fill="#9aceec" />
        <polygon points="12,21 32,32 32,54 12,43" fill="#15678b" />
        <polygon points="52,21 32,32 32,54 52,43" fill="#208cb7" />
      </g>
    </svg>
  );
}
