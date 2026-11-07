// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// status-grid -- one tile per device of an anchor for ONE measurement, each tile coloured by the
// threshold band its device's latest reading is in. It answers "which of my four hundred devices
// need me" at a glance, where the fleet table answers "what are their numbers".
//
//   - a tile with no number is HOLLOW and dashed, never green: a device that has not reported is not
//     a device that is fine, and the legend counts it apart ("No reading");
//   - the colour is never the only cue: each tile prints the device name and the value, and carries
//     its band as text in its accessible label and its legend swatch;
//   - past MAX_TILES the grid says how many devices it left out (the fleet table is the tool there);
//   - all DOM, so the readability gate measures every word.

import { useMemo, type ReactNode } from 'react';

import {
  css,
  fontVar,
  formatValue,
  levelColor,
  optBoolean,
  optNumber,
  optString,
  optThresholds,
  spaceVar,
  useSeverityColors,
  WidgetFrame,
  type WidgetProps,
  type WidgetScale,
} from '@devicechain/widget-sdk';

import { fleetStrings, type FleetStrings } from '../fleet-strings';
import { LevelEmpty } from '../level-readout';
import { Explained } from '../no-reading';
import { MAX_TILES, buildTiles, tally, tileSide, type Tile } from '../status-grid-model';
import { statusGridStrings } from '../status-grid-strings';

const MIN_SIDE_PX = 64;
const MAX_SIDE_PX = 160;
// From this tile size a tile that is not OK prints its state under the value.
const SAY_STATE_FROM_PX = 72;

export function StatusGrid({ widget, data }: WidgetProps) {
  const strings = fleetStrings();
  const fleet = data.members;
  const title = optString(widget.options, 'title');
  const frame = (child: ReactNode) => (
    <WidgetFrame title={title} flush>
      {child}
    </WidgetFrame>
  );
  const measurement = optString(widget.options, 'measurement') ?? widget.datasource?.measurements[0];
  if (!fleet || fleet.status === 'loading') return frame(<LevelEmpty message={strings.loading} />);
  if (fleet.status === 'unbound') return frame(<Explained kind="unbound" message={strings.unbound} />);
  if (fleet.status === 'forbidden') return frame(<Explained kind="forbidden" message={strings.forbidden} />);
  if (fleet.status === 'unsupported') return frame(<LevelEmpty message={strings.unsupported} />);
  if (!measurement) return frame(<LevelEmpty message={statusGridStrings().chooseMeasurement} />);
  if (fleet.members.length === 0) return frame(<LevelEmpty message={strings.noMembers} />);
  return (
    <WidgetFrame title={title} flush>
      {(ws) => <Grid widget={widget} fleet={fleet} measurement={measurement} ws={ws} />}
    </WidgetFrame>
  );
}

function Grid({
  widget,
  fleet,
  measurement,
  ws,
}: {
  widget: WidgetProps['widget'];
  fleet: NonNullable<WidgetProps['data']['members']>;
  measurement: string;
  ws: WidgetScale;
}) {
  const colors = useSeverityColors();
  const strings = fleetStrings();
  const words = statusGridStrings();
  const unit = optString(widget.options, 'unit');
  const precision = optNumber(widget.options, 'precision');
  const bands = useMemo(() => optThresholds(widget.options, 'thresholds'), [widget.options]);
  const worstFirst = optBoolean(widget.options, 'worstFirst') === true;
  const all = useMemo(() => buildTiles(fleet.members, measurement, bands, worstFirst), [fleet.members, measurement, bands, worstFirst]);
  const drawn = all.slice(0, MAX_TILES);
  const legend = useMemo(() => tally(all), [all]);
  const omitted = Math.max(fleet.total, all.length) - drawn.length;
  const gap = ws.px('space-1');
  const legendH = ws.px('font-xs') * 2.4;
  const noteH = omitted > 0 ? ws.px('font-xs') * 1.8 : 0;
  const side = tileSide(
    drawn.length,
    ws.size.width - gap * 2,
    ws.size.height - legendH - noteH - gap * 2,
    Math.round(MIN_SIDE_PX * ws.scale),
    Math.round(MAX_SIDE_PX * ws.scale),
    gap,
  );

  const groupName = (g: Tile['group']): string => (g === 'none' ? words.none : g === 'ok' ? words.ok : (words.levels[g] ?? g));

  return (
    <div style={{ height: '100%', display: 'flex', flexDirection: 'column', boxSizing: 'border-box', color: css('foreground') }}>
      <ul
        data-status-legend=""
        style={{ listStyle: 'none', margin: 0, padding: `${gap}px`, display: 'flex', flexWrap: 'wrap', gap: spaceVar(3), fontSize: fontVar('xs') }}
      >
        {legend.map((l) => (
          <li key={l.group} data-legend-group={l.group} style={{ display: 'flex', alignItems: 'center', gap: spaceVar(1) }}>
            <span
              aria-hidden="true"
              style={{
                width: '0.8em',
                height: '0.8em',
                borderRadius: 2,
                boxSizing: 'border-box',
                // The swatch is the tile's own look: OK tiles are neutral, a device with no reading is hollow.
                background: l.group === 'none' ? 'transparent' : l.group === 'ok' ? css('muted') : (levelColor(colors, l.group) ?? css('muted')),
                border: l.group === 'none' ? `1px dashed ${css('muted-foreground')}` : l.group === 'ok' ? `2px solid ${css('border')}` : undefined,
              }}
            />
            <span>
              {groupName(l.group)} <b style={{ fontVariantNumeric: 'tabular-nums' }}>{l.count}</b>
            </span>
          </li>
        ))}
      </ul>
      <div
        data-status-grid=""
        data-tiles={drawn.length}
        style={{
          flex: '1 1 0',
          minHeight: 0,
          overflow: 'auto',
          padding: `${gap}px`,
          display: 'grid',
          gridTemplateColumns: `repeat(auto-fill, ${side}px)`,
          gridAutoRows: `${side}px`,
          gap,
          alignContent: 'start',
        }}
      >
        {drawn.map((t) => {
          const color = levelColor(colors, t.band?.level);
          const has = t.cell.kind === 'value';
          const stateName = t.band?.label ?? groupName(t.group);
          // A tile that is not simply OK also SAYS its state, so colour is never the only cue. Only when
          // the tile is big enough to hold a third line (an unmeasured body takes the full presentation).
          const sayState = has && t.group !== 'ok' && (side >= SAY_STATE_FROM_PX || ws.size.width === 0);
          const reading = t.cell.kind === 'value' ? `${formatValue(t.cell.value, precision)}${unit ? ` ${unit}` : ''}` : reasonOf(t, strings);
          return (
            <div
              key={t.token}
              role="group"
              aria-label={words.tile(t.label, reading, stateName)}
              title={`${t.label}\n${reading}
${stateName}`}
              data-tile={t.token}
              data-threshold-level={t.band?.level}
              data-tile-group={t.group}
              style={{
                boxSizing: 'border-box',
                minWidth: 0,
                overflow: 'hidden',
                padding: spaceVar(1),
                borderRadius: 4,
                display: 'flex',
                flexDirection: 'column',
                justifyContent: 'space-between',
                background: has ? (color ? `color-mix(in srgb, ${color} 22%, ${css('card')})` : css('muted')) : 'transparent',
                border: has ? `2px solid ${color ?? css('border')}` : `1px dashed ${css('muted-foreground')}`,
              }}
            >
              <span style={{ fontSize: fontVar('xs'), overflowWrap: 'anywhere', lineHeight: 1.15, color: css('muted-foreground') }}>{t.label}</span>
              <span style={{ fontSize: has ? fontVar('md') : fontVar('xs'), fontWeight: 600, fontVariantNumeric: 'tabular-nums', overflowWrap: 'anywhere', lineHeight: 1.15 }}>
                {has ? reading : '—'}
              </span>
              {sayState && (
                <span data-tile-state="" style={{ fontSize: fontVar('xs'), lineHeight: 1.1, overflowWrap: 'anywhere', color: css('muted-foreground') }}>
                  {stateName}
                </span>
              )}
            </div>
          );
        })}
      </div>
      {omitted > 0 && (
        <div data-status-truncated="" style={{ padding: `0 ${gap}px ${gap}px`, fontSize: fontVar('xs'), color: css('muted-foreground') }}>
          {strings.truncated(drawn.length, Math.max(fleet.total, all.length))}
        </div>
      )}
    </div>
  );
}

function reasonOf(t: Tile, strings: FleetStrings): string {
  switch (t.cell.kind) {
    case 'not-reported':
      return strings.notReported;
    case 'unread':
      return strings.unread;
    case 'failed':
      return strings.failed;
    case 'loading':
      return strings.loading;
    default:
      return strings.noData;
  }
}
