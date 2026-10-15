// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The embed's fixed canvas: its children are laid out at exactly the canvas size, then the
// whole canvas is scaled to fit the host box and centred in it (see fitCanvas). The host's
// size only ever changes the scale.

import { useLayoutEffect, useRef, useState, type ReactNode } from 'react';

import { fitCanvas, type CanvasSize } from './embed';

export function EmbedStage({ canvas, children }: { canvas: CanvasSize; children: ReactNode }) {
  const host = useRef<HTMLDivElement>(null);
  const [box, setBox] = useState<CanvasSize>(canvas);

  useLayoutEffect(() => {
    const el = host.current;
    if (!el) return;
    const measure = () => setBox({ width: el.clientWidth, height: el.clientHeight });
    measure();
    if (typeof ResizeObserver === 'undefined') return;
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, []);

  const { scale, left, top } = fitCanvas(box, canvas);
  return (
    <div ref={host} data-testid="embed-host" style={{ position: 'relative', width: '100%', height: '100%', overflow: 'hidden' }}>
      <div
        data-testid="embed-canvas"
        data-scale={scale}
        style={{
          position: 'absolute',
          left,
          top,
          width: canvas.width,
          height: canvas.height,
          transform: `scale(${scale})`,
          transformOrigin: '0 0',
          display: 'flex',
          flexDirection: 'column',
          overflow: 'hidden',
          background: 'hsl(var(--background))',
        }}
      >
        {children}
      </div>
    </div>
  );
}
