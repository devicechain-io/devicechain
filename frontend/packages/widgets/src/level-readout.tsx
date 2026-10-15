// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The fitted DOM readout and the explained empty state the level-and-indicator widgets share.
// DOM, not canvas text, so the readability gate can measure it.

import {
  css,
  fitValueFont,
  fitValueFontCss,
  FIT_MIN_FONT_PX,
  fontVar,
  spaceVar,
  useStickyChars,
} from '@devicechain/widget-sdk';

import type { LevelStrings } from './level-strings';
import type { ScalePosition } from './value-scale';

// The room the readout may fill (a box inside the frame's content column) and the density scale.
export interface ReadoutRoom {
  size: { width: number; height: number };
  scale: number;
}

export interface LevelReadoutProps extends ReadoutRoom {
  text: string;
  unit?: string;
  color?: string;
  // Set when the value lies off the scale: a marker is drawn beside it.
  position?: ScalePosition;
  strings: LevelStrings;
}

const MARKER = { above: '▲', below: '▼' } as const;

export function LevelReadout({ text, unit, color, position, size, scale, strings }: LevelReadoutProps) {
  const hasUnit = unit !== undefined && unit !== '';
  const marked = position === 'above' || position === 'below';
  // The unit and the marker render at half size, so each counts for half its characters.
  const chars = useStickyChars(text.length + (hasUnit ? Math.ceil(unit.length / 2) + 1 : 0) + (marked ? 1 : 0));
  const floor = Math.round(FIT_MIN_FONT_PX * scale);
  const measured = size.width > 0 && size.height > 0;
  const px = measured ? fitValueFont(size, chars, { floor }) : undefined;
  const font = px ?? fitValueFontCss(chars, { floor });
  const half = px !== undefined ? Math.round(px / 2) : `calc(${font} / 2)`;
  return (
    <div
      data-level-readout=""
      style={{
        fontSize: font,
        fontWeight: 600,
        lineHeight: 1.1,
        whiteSpace: 'nowrap',
        fontVariantNumeric: 'tabular-nums',
        color: color ?? css('foreground'),
        textAlign: 'center',
      }}
    >
      {text}
      {hasUnit ? (
        <span style={{ fontSize: half, color: css('muted-foreground'), marginLeft: spaceVar(1) }}>{unit}</span>
      ) : null}
      {marked ? (
        <span
          role="img"
          aria-label={position === 'above' ? strings.aboveScale : strings.belowScale}
          title={position === 'above' ? strings.aboveScale : strings.belowScale}
          style={{ fontSize: half, color: css('muted-foreground'), marginLeft: spaceVar(1) }}
        >
          {MARKER[position]}
        </span>
      ) : null}
    </div>
  );
}

// Why there is nothing to draw: a short sentence in the widget's own box, never a fake value.
export function LevelEmpty({ message }: { message: string }) {
  return (
    <div
      data-level-empty=""
      style={{
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        height: '100%',
        textAlign: 'center',
        color: css('muted-foreground'),
        fontSize: fontVar('sm'),
        overflowWrap: 'anywhere',
      }}
    >
      {message}
    </div>
  );
}
