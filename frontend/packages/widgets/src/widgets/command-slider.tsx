// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// command-slider — sends a command carrying ONE numeric parameter, chosen with a slider. The range
// comes from the command definition baked into the options (the same snapshot the command button
// renders its form from); the author's min/max/step fill only what the definition leaves open.
//
// 🔴 DRAGGING SENDS NOTHING. The thumb stages a value; releasing it (or pressing Enter, for a
// keyboard user) sends it, once. While a send is in flight the slider is disabled, so a second
// release cannot queue a second command.
//
// 🔴 IT SHOWS WHAT WAS SENT, NOT WHAT THE DEVICE IS DOING. The thumb rests at the last value sent
// (read off the command history) and the caption says so with that command's delivery status.
// With nothing sent the thumb rests at the bottom of the range and the readout is a dash.

import { useId, useMemo, useRef, useState, type ReactNode } from 'react';

import {
  css,
  FIT_MIN_FONT_PX,
  fitValueFont,
  fitValueFontCss,
  fontVar,
  optNumber,
  optString,
  spaceVar,
  useStickyChars,
  WidgetFrame,
  type CommandStreamState,
  type WidgetProps,
  type WidgetScale,
} from '@devicechain/widget-sdk';

import {
  bindControl,
  clampToRange,
  lastSent,
  payloadFor,
  type Binding,
} from '../command-control-core';
import {
  ControlEmpty,
  ControlNotes,
  LastSentCaption,
  refusalText,
  TOUCH_TARGET_PX,
  useCommandSender,
  type CommandSender,
} from '../command-control';

type Bound = Extract<Binding, { ok: true }>;

const withUnit = (v: number, unit: string | undefined) => (unit ? `${v} ${unit}` : String(v));

export function CommandSlider({ widget, data, actions }: WidgetProps<CommandStreamState>) {
  const commandName = optString(widget.options, 'commandName');
  const commandLabel = optString(widget.options, 'commandLabel') ?? commandName;
  const parameterSchema = optString(widget.options, 'parameterSchema') ?? null;
  const parameter = optString(widget.options, 'parameter');
  const min = optNumber(widget.options, 'min');
  const max = optNumber(widget.options, 'max');
  const step = optNumber(widget.options, 'step');

  const binding = useMemo(
    () => bindControl('number', { commandName, parameterSchema, parameter, min, max, step }),
    [commandName, parameterSchema, parameter, min, max, step],
  );
  const sender = useCommandSender(
    actions,
    data.deviceToken,
    commandName,
    `${commandName ?? ''}::${parameterSchema ?? ''}::${parameter ?? ''}`,
  );

  const last =
    binding.ok && commandName
      ? lastSent('number', data.commands, commandName, binding.param.name, sender.optimistic)
      : null;

  // The staged value. A ref mirrors it so a release reads what the last change set, not the value
  // the previous render closed over.
  const [draft, setDraftState] = useState<number | null>(null);
  const draftRef = useRef<number | null>(null);
  const setDraft = (v: number | null) => {
    draftRef.current = v;
    setDraftState(v);
  };
  const captionId = useId();

  const commit = () => {
    if (!binding.ok) return;
    const staged = draftRef.current;
    if (staged === null || !sender.canSend) return;
    if (sender.send(payloadFor(binding, staged), staged)) setDraft(null);
  };

  return (
    <WidgetFrame title={optString(widget.options, 'title')}>
      {(scale) =>
        !binding.ok ? (
          <ControlEmpty message={refusalText(binding.refusal, sender.strings, 'number')} />
        ) : (
          <SliderBody
            binding={binding}
            label={commandLabel ?? binding.param.name}
            draft={draft}
            lastValue={typeof last?.value === 'number' ? last.value : undefined}
            sender={sender}
            captionId={captionId}
            caption={
              <LastSentCaption
                id={captionId}
                last={last}
                strings={sender.strings}
                truncated={data.total > data.commands.length}
                format={(v) => (typeof v === 'number' ? withUnit(v, binding.param.unit) : sender.strings.noValue)}
                override={draft !== null ? sender.strings.unsent : sender.pending ? sender.strings.sending : undefined}
              />
            }
            onStage={(v) => setDraft(clampToRange(binding.range!, v))}
            onCommit={commit}
            scale={scale}
          />
        )
      }
    </WidgetFrame>
  );
}

function SliderBody({
  binding,
  label,
  draft,
  lastValue,
  sender,
  captionId,
  caption,
  onStage,
  onCommit,
  scale: ws,
}: {
  binding: Bound;
  label: string;
  draft: number | null;
  lastValue: number | undefined;
  sender: CommandSender;
  captionId: string;
  caption: ReactNode;
  onStage: (v: number) => void;
  onCommit: () => void;
  scale: WidgetScale;
}) {
  const range = binding.range!;
  const unit = binding.param.unit;
  const disabled = !sender.canSend || sender.pending;
  const rest = lastValue !== undefined ? clampToRange(range, lastValue) : range.min;
  const shown = draft ?? rest;
  const known = draft !== null || lastValue !== undefined;
  const readout = known ? String(shown) : '—';

  // The readout fits the room left beside the track, with the same curve the value widgets use.
  const chars = useStickyChars(readout.length + (unit && known ? Math.ceil(unit.length / 2) + 1 : 0));
  const floor = Math.round(FIT_MIN_FONT_PX * ws.scale);
  const measured = ws.size.width > 0 && ws.size.height > 0;
  const room = {
    width: Math.floor(ws.size.width * 0.34),
    height: Math.max(TOUCH_TARGET_PX, Math.floor(ws.size.height * 0.5)),
  };
  const font = measured ? fitValueFont(room, chars, { floor }) : fitValueFontCss(chars, { floor });

  return (
    <div
      style={{
        height: '100%',
        boxSizing: 'border-box',
        display: 'flex',
        flexDirection: 'column',
        justifyContent: 'safe center',
        gap: spaceVar(1),
        overflow: 'hidden',
      }}
    >
      <div style={{ display: 'flex', alignItems: 'center', gap: spaceVar(2), minHeight: TOUCH_TARGET_PX }}>
        <input
          type="range"
          aria-label={label}
          aria-valuetext={withUnit(shown, unit)}
          aria-describedby={captionId}
          min={range.min}
          max={range.max}
          step={range.step}
          value={shown}
          disabled={disabled}
          data-staged={draft !== null ? 'true' : undefined}
          onChange={(e) => onStage(Number(e.target.value))}
          onPointerUp={onCommit}
          onKeyDown={(e) => {
            if (e.key === 'Enter') onCommit();
          }}
          style={{
            flex: '1 1 0',
            minWidth: 0,
            height: TOUCH_TARGET_PX,
            margin: 0,
            accentColor: css('primary'),
            cursor: disabled ? 'not-allowed' : 'pointer',
          }}
        />
        <output
          aria-hidden="true"
          data-slider-readout=""
          style={{
            flex: '0 0 auto',
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
        </output>
      </div>
      {caption}
      <ControlNotes sender={sender} />
    </div>
  );
}
