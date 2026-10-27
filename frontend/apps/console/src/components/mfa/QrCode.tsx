// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useMemo } from 'react';
import qrcode from 'qrcode-generator';

// A QR code drawn in the browser from the string it is given.
//
// 🔴 NOTHING LEAVES THE PAGE. The value is an otpauth:// URI that carries the shared secret, so
// it is encoded here, in memory, and never handed to an image service, a URL or a log. The
// drawing is plain SVG elements built by React from the module matrix (no innerHTML, no
// canvas, no data: URL), so it is gone from the DOM the moment the component unmounts.
//
// It is drawn dark-on-white with its own quiet zone whatever the theme: a scanner needs the
// contrast and the margin, and a themed (inverted) code is the one a phone app fails to read.
const QUIET_ZONE = 4;

export function QrCode({ value, label, size = 192 }: { value: string; label: string; size?: number }) {
  const { count, path } = useMemo(() => {
    // Type 0 picks the smallest version that fits; M recovers from ~15% damage, which is
    // plenty for a code shown on a screen.
    const qr = qrcode(0, 'M');
    qr.addData(value, 'Byte');
    qr.make();
    const n = qr.getModuleCount();
    // One path of 1x1 squares: a row's horizontal run is a single rectangle.
    let d = '';
    for (let r = 0; r < n; r++) {
      let c = 0;
      while (c < n) {
        if (!qr.isDark(r, c)) {
          c++;
          continue;
        }
        const start = c;
        while (c < n && qr.isDark(r, c)) c++;
        d += `M${start + QUIET_ZONE} ${r + QUIET_ZONE}h${c - start}v1h${start - c}z`;
      }
    }
    return { count: n, path: d };
  }, [value]);

  const side = count + QUIET_ZONE * 2;
  return (
    <svg
      role="img"
      aria-label={label}
      viewBox={`0 0 ${side} ${side}`}
      width={size}
      height={size}
      shapeRendering="crispEdges"
      className="rounded-md border border-border"
    >
      <rect width={side} height={side} fill="#ffffff" />
      <path d={path} fill="#000000" />
    </svg>
  );
}
