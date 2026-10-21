// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// command-slider — sends a command carrying ONE numeric parameter, chosen with a slider. The range
// comes from the command definition baked into the options (the same snapshot the command button
// renders its form from); the author's min/max/step fill only what the definition leaves open.
//
// 🔴 DRAGGING SENDS NOTHING. The thumb stages a value; releasing it on the slider (or pressing
// Enter, for a keyboard user) sends it, once. A staged value that is not committed is DISCARDED,
// never sent later: a cancelled pointer (the browser took the gesture for a scroll), a lost
// capture or a release off the slider, Escape, or focus leaving the slider. While a send is in
// flight the slider is aria-disabled and ignores input, so a second release cannot queue a second
// command; it is not `disabled`, which would drop a keyboard user's focus on the page body.
//
// Keyboard: the arrow, Page and Home/End keys stage a value (the native range input's own
// behaviour, so aria-valuetext follows the staged value) and Enter sends it. That departure from a
// plain slider is announced: the input is described by a hint saying so, as well as by the
// last-sent caption, which reads "Not sent yet. ... press Enter to send" while a value is staged.
//
// 🔴 IT SHOWS WHAT WAS SENT, NOT WHAT THE DEVICE IS DOING. The thumb rests at the last value sent
// (read off the command history) and the caption says so with that command's delivery status.
// With nothing sent the thumb rests at the bottom of the range and the readout is a dash.

import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from 'react';

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
import { StepperBody } from './command-stepper';
import {
  ControlEmpty,
  ControlNotes,
  readOnlyId,
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
  // Additive: absent, or anything but 'stepper', is the slider as before.
  const stepper = optString(widget.options, 'mode') === 'stepper';

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
  const hintId = useId();

  // A pointer pressed on the slider whose release has not been seen. A release anywhere but on
  // the slider, a cancel or a lost capture while it is set discards the staged value.
  const pressed = useRef(false);
  const offRelease = useRef<(() => void) | null>(null);
  useEffect(() => () => offRelease.current?.(), []);
  const discard = () => {
    pressed.current = false;
    offRelease.current?.();
    offRelease.current = null;
    if (draftRef.current !== null) setDraft(null);
  };
  const press = (input: HTMLInputElement) => {
    pressed.current = true;
    offRelease.current?.();
    const doc = input.ownerDocument;
    const onRelease = (e: PointerEvent) => {
      // The slider's own release commits through onPointerUp; this one sees releases elsewhere.
      if (e.target instanceof Node && input.contains(e.target)) return;
      discard();
    };
    doc.addEventListener('pointerup', onRelease, true);
    offRelease.current = () => doc.removeEventListener('pointerup', onRelease, true);
  };

  const commit = () => {
    if (!binding.ok) return;
    const staged = draftRef.current;
    if (staged === null || !sender.canSend) return;
    if (sender.send(payloadFor(binding, staged), staged)) setDraft(null);
  };
  const release = () => {
    pressed.current = false;
    offRelease.current?.();
    offRelease.current = null;
    commit();
  };

  const caption = binding.ok ? (
    <LastSentCaption
      id={captionId}
      last={last}
      strings={sender.strings}
      truncated={data.total > data.commands.length}
      format={(v) => (typeof v === 'number' ? withUnit(v, binding.param.unit) : sender.strings.noValue)}
      override={
        draft !== null
          ? stepper
            ? sender.strings.stepperUnsent
            : sender.strings.unsent
          : sender.pending
            ? sender.strings.sending
            : undefined
      }
    />
  ) : null;
  const onStage = (v: number) => {
    // A value moved while a send is in flight is not staged: the control is aria-disabled.
    if (binding.ok && !sender.pending) setDraft(clampToRange(binding.range!, v));
  };

  return (
    <WidgetFrame title={optString(widget.options, 'title')}>
      {(scale) => {
        if (!binding.ok) return <ControlEmpty message={refusalText(binding.refusal, sender.strings, 'number')} />;
        const common = {
          binding,
          label: commandLabel ?? binding.param.name,
          draft,
          lastValue: typeof last?.value === 'number' ? last.value : undefined,
          sender,
          captionId,
          hintId,
          caption,
          onStage,
          onCommit: commit,
          onDiscard: discard,
          scale,
        };
        return stepper ? (
          <StepperBody {...common} />
        ) : (
          <SliderBody
            {...common}
            onPress={press}
            onRelease={release}
            onLostCapture={() => {
              if (pressed.current) discard();
            }}
          />
        );
      }}
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
  hintId,
  caption,
  onStage,
  onCommit,
  onPress,
  onRelease,
  onDiscard,
  onLostCapture,
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
  onPress: (input: HTMLInputElement) => void;
  onRelease: () => void;
  onDiscard: () => void;
  onLostCapture: () => void;
  scale: WidgetScale;
}) {
  const range = binding.range!;
  const unit = binding.param.unit;
  // Read-only is `disabled`; in flight is only aria-disabled, so focus stays on the slider.
  const disabled = !sender.canSend;
  const busy = sender.pending;
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
          aria-describedby={`${hintId} ${captionId}${sender.readOnlyReason ? ` ${readOnlyId(captionId)}` : ''}`}
          aria-disabled={busy || undefined}
          aria-busy={busy || undefined}
          min={range.min}
          max={range.max}
          step={range.step}
          value={shown}
          disabled={disabled}
          data-staged={draft !== null ? 'true' : undefined}
          onChange={(e) => onStage(Number(e.target.value))}
          onPointerDown={(e) => onPress(e.currentTarget)}
          onPointerUp={onRelease}
          onPointerCancel={onDiscard}
          onLostPointerCapture={onLostCapture}
          onBlur={onDiscard}
          onKeyDown={(e) => {
            if (e.key === 'Enter') onCommit();
            else if (e.key === 'Escape') onDiscard();
          }}
          style={{
            flex: '1 1 0',
            minWidth: 0,
            height: TOUCH_TARGET_PX,
            margin: 0,
            accentColor: css('primary'),
            cursor: disabled || busy ? 'not-allowed' : 'pointer',
          }}
        />
        <span id={hintId} hidden>
          {sender.strings.sliderHint}
        </span>
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
      {/* One row, so the short read-only caption costs no height at a two-row widget's minimum span. */}
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'baseline', columnGap: spaceVar(2) }}>
        {caption}
        <ControlNotes sender={sender} captionId={captionId} />
      </div>
    </div>
  );
}
