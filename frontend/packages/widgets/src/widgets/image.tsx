// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// image — a static image on the canvas (floor plans, logos, SCADA backdrops). No
// datasource; the Phase-4 SCADA symbol family will layer live SVG on top of this.

import type { CSSProperties } from 'react';

import { WidgetFrame } from '../frame';
import { css } from '../theme';
import { optString, type WidgetProps } from '../widget';

const FITS = new Set(['contain', 'cover', 'fill']);

export function Image({ widget }: WidgetProps) {
  const url = optString(widget.options, 'url');
  const alt = optString(widget.options, 'alt') ?? '';
  const fitOption = optString(widget.options, 'fit');
  const objectFit = (fitOption && FITS.has(fitOption) ? fitOption : 'contain') as CSSProperties['objectFit'];

  return (
    <WidgetFrame title={optString(widget.options, 'title')}>
      {url ? (
        <img src={url} alt={alt} style={{ width: '100%', height: '100%', objectFit, display: 'block' }} />
      ) : (
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            height: '100%',
            color: css('muted-foreground'),
            fontSize: 13,
          }}
        >
          No image URL
        </div>
      )}
    </WidgetFrame>
  );
}
