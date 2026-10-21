// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The command slider's stepper mode: − / value / + and a Send button, for a touch wall where
// dragging a thumb is too fine. The range and step come from the command parameter's declared
// schema, exactly as the slider's do (command-slider.tsx binds them and owns the staged value).
//
// 🔴 PRESSING − OR + SENDS NOTHING. It stages a value; only Send (or Enter on the value) sends it,
// once. A command that steps a setpoint or a rate is acted on by the device, and a held-down or
// double-tapped button must not become a burst of commands. The explicit Send is also where a
// disruptive command is chosen on purpose (the platform then holds it for the maintenance window).
// Escape discards a staged value. Focus moving to the Send button does NOT discard it.

import type { KeyboardEvent, ReactNode } from 'react';

import {
  css,
  FIT_MIN_FONT_PX,
  fitValueFont,
  fitValueFontCss,
  fontVar,
  spaceVar,
  useStickyChars,
  type WidgetScale,
} from '@devicechain/widget-sdk';

import { clampToRange, type Binding } from '../command-control-core';
import { ControlNotes, readOnlyId, TOUCH_TARGET_PX, type CommandSender } from '../command-control';

type Bound = Extract<Binding, { ok: true }>;

const PAGE_STEPS = 10;

const button = (off: boolean) =>
  ({
    flex: '0 0 auto',
    boxSizing: 'border-box',
    minWidth: TOUCH_TARGET_PX,
    height: TOUCH_TARGET_PX,
    padding: `0 ${spaceVar(2)}`,
    border: `1px solid ${css('border')}`,
    borderRadius: 6,
    background: css('card'),
    color: css('foreground'),
    font: 'inherit',
    fontSize: fontVar('md'),
    fontWeight: 600,
    cursor: off ? 'not-allowed' : 'pointer',
    opacity: off ? 0.5 : 1,
  }) as const;

export function StepperBody({
  binding,
  label,
  draft,
  lastValue,
  sender,
  captionId,
  hintId,
  caption,
  onStage,
  onCommit,
  onDiscard,
  scale: ws,
}: {
  binding: Bound;
  label: string;
  draft: number | null;
  lastValue: number | undefined;
  sender: CommandSender;
  captionId: string;
  hintId: string;
  caption: ReactNode;
  onStage: (v: number) => void;
  onCommit: () => void;
  onDiscard: () => void;
  scale: WidgetScale;
}) {
  const range = binding.range!;
  const unit = binding.param.unit;
  const busy = sender.pending;
  const inert = !sender.canSend || busy;
  const rest = lastValue !== undefined ? clampToRange(range, lastValue) : range.min;
  const shown = draft ?? rest;
  const known = draft !== null || lastValue !== undefined;
  const readout = known ? String(shown) : '—';
  const text = unit && known ? `${shown} ${unit}` : String(shown);

  // clampToRange snaps to the grid from min and trims float noise, so 0.1 steps stay 0.3, not 0.30000000000000004.
  const by = (steps: number) => {
    if (inert) return;
    const next = clampToRange(range, shown + steps * range.step);
    if (next !== shown || draft === null) onStage(next);
  };
  const canSendNow = draft !== null && !inert;

  const chars = useStickyChars(readout.length + (unit && known ? Math.ceil(unit.length / 2) + 1 : 0));
  const floor = Math.round(FIT_MIN_FONT_PX * ws.scale);
  const measured = ws.size.width > 0 && ws.size.height > 0;
  const font = measured
    ? fitValueFont({ width: Math.floor(ws.size.width * 0.3), height: Math.max(TOUCH_TARGET_PX, Math.floor(ws.size.height * 0.5)) }, chars, { floor })
    : fitValueFontCss(chars, { floor });

  const key = (e: KeyboardEvent) => {
    const k = e.key;
    if (k === 'ArrowUp' || k === 'ArrowRight') by(1);
    else if (k === 'ArrowDown' || k === 'ArrowLeft') by(-1);
    else if (k === 'PageUp') by(PAGE_STEPS);
    else if (k === 'PageDown') by(-PAGE_STEPS);
    else if (k === 'Home') inert || onStage(range.min);
    else if (k === 'End') inert || onStage(range.max);
    else if (k === 'Enter') onCommit();
    else if (k === 'Escape') onDiscard();
    else return;
    e.preventDefault();
  };

  const atMin = shown <= range.min;
  const atMax = shown >= range.max;
  return (
    <div
      style={{ height: '100%', boxSizing: 'border-box', display: 'flex', flexDirection: 'column', justifyContent: 'safe center', gap: spaceVar(1), overflow: 'hidden' }}
    >
      <div style={{ display: 'flex', alignItems: 'center', gap: spaceVar(1), minHeight: TOUCH_TARGET_PX }}>
        <button
          type="button"
          tabIndex={-1}
          aria-label={sender.strings.decrease}
          aria-disabled={inert || atMin || undefined}
          data-step="down"
          onClick={() => by(-1)}
          style={button(inert || atMin)}
        >
          −
        </button>
        <div
          role="spinbutton"
          tabIndex={0}
          aria-label={label}
          aria-valuemin={range.min}
          aria-valuemax={range.max}
          aria-valuenow={shown}
          aria-valuetext={text}
          aria-describedby={`${hintId} ${captionId}${sender.readOnlyReason ? ` ${readOnlyId(captionId)}` : ''}`}
          aria-disabled={inert || undefined}
          aria-busy={busy || undefined}
          data-staged={draft !== null ? 'true' : undefined}
          onKeyDown={key}
          style={{
            flex: '1 1 0',
            minWidth: 0,
            textAlign: 'center',
            fontSize: font,
            fontWeight: 600,
            lineHeight: 1.1,
            whiteSpace: 'nowrap',
            fontVariantNumeric: 'tabular-nums',
            color: draft !== null ? css('foreground') : css('muted-foreground'),
          }}
        >
          {readout}
          {unit && known ? (
            <span style={{ fontSize: fontVar('sm'), marginLeft: spaceVar(1), color: css('muted-foreground') }}>{unit}</span>
          ) : null}
        </div>
        <button
          type="button"
          tabIndex={-1}
          aria-label={sender.strings.increase}
          aria-disabled={inert || atMax || undefined}
          data-step="up"
          onClick={() => by(1)}
          style={button(inert || atMax)}
        >
          +
        </button>
        <button
          type="button"
          aria-disabled={!canSendNow || undefined}
          data-step="send"
          onClick={() => canSendNow && onCommit()}
          style={{ ...button(!canSendNow), background: canSendNow ? css('primary') : css('card'), color: canSendNow ? css('primary-foreground') : css('foreground') }}
        >
          {sender.strings.send}
        </button>
        <span id={hintId} hidden>
          {sender.strings.stepperHint}
        </span>
      </div>
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'baseline', columnGap: spaceVar(2) }}>
        {caption}
        <ControlNotes sender={sender} captionId={captionId} />
      </div>
    </div>
  );
}
