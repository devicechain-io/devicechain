// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// command-toggle — sends a command carrying ONE boolean parameter, one command per click.
//
// 🔴 THE SWITCH SHOWS THE LAST VALUE SENT, NOT THE DEVICE'S STATE. It rests where the newest
// command left it (read off the command history) and the caption says "Last sent" with that
// command's delivery status.
//
// 🔴 IT NEVER GUESSES WHICH WAY TO FLIP. A click sends the opposite of the last value sent only
// when that value is a live intent: a command that is pending or was delivered, carrying a value
// this control can read. With nothing sent, a history too short to show this command, a newest
// command with no readable value, or a newest command that FAILED (its value never reached the
// device, so flipping relative to it would send what the operator may already have), the switch
// is replaced by two explicit buttons, On and Off, and the operator chooses.
//
// A click while a send is in flight does nothing, so rapid clicks make one command, not a queue of
// alternating ones.

import { useId, useMemo, type ReactNode } from 'react';

import {
  css,
  FIT_MIN_FONT_PX,
  fitValueFont,
  fitValueFontCss,
  optString,
  spaceVar,
  useStickyChars,
  WidgetFrame,
  type CommandStreamState,
  type WidgetProps,
  type WidgetScale,
} from '@devicechain/widget-sdk';

import { bindControl, deliveryGroup, lastSent, payloadFor, type Binding, type LastSent } from '../command-control-core';
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

const TRACK_W = 52;
const TRACK_H = 30;
const THUMB = 24;

export function CommandToggle({ widget, data, actions }: WidgetProps<CommandStreamState>) {
  const commandName = optString(widget.options, 'commandName');
  const commandLabel = optString(widget.options, 'commandLabel') ?? commandName;
  const parameterSchema = optString(widget.options, 'parameterSchema') ?? null;
  const parameter = optString(widget.options, 'parameter');

  const binding = useMemo(
    () => bindControl('boolean', { commandName, parameterSchema, parameter }),
    [commandName, parameterSchema, parameter],
  );
  const sender = useCommandSender(
    actions,
    data.deviceToken,
    commandName,
    `${commandName ?? ''}::${parameterSchema ?? ''}::${parameter ?? ''}`,
  );
  const last =
    binding.ok && commandName
      ? lastSent('boolean', data.commands, commandName, binding.param.name, sender.optimistic)
      : null;
  const captionId = useId();

  return (
    <WidgetFrame title={optString(widget.options, 'title')}>
      {(scale) =>
        !binding.ok ? (
          <ControlEmpty message={refusalText(binding.refusal, sender.strings, 'boolean')} />
        ) : (
          <ToggleBody
            binding={binding}
            label={commandLabel ?? binding.param.name}
            value={intentOf(last)}
            sender={sender}
            captionId={captionId}
            caption={
              <LastSentCaption
                id={captionId}
                last={last}
                strings={sender.strings}
                truncated={data.total > data.commands.length}
                format={(v) => (typeof v === 'boolean' ? (v ? sender.strings.on : sender.strings.off) : sender.strings.noValue)}
                override={sender.pending ? sender.strings.sending : undefined}
              />
            }
            scale={scale}
          />
        )
      }
    </WidgetFrame>
  );
}

// The value a flip may be computed from, or undefined when the operator has to choose. Only a
// command that is still on its way, or arrived, states an intent the device is acting on.
export function intentOf(last: LastSent | null): boolean | undefined {
  if (!last || typeof last.value !== 'boolean') return undefined;
  const group = deliveryGroup(last.status);
  return group === 'pending' || group === 'delivered' ? last.value : undefined;
}

function ToggleBody({
  binding,
  label,
  value,
  sender,
  captionId,
  caption,
  scale: ws,
}: {
  binding: Bound;
  label: string;
  value: boolean | undefined;
  sender: CommandSender;
  captionId: string;
  caption: ReactNode;
  scale: WidgetScale;
}) {
  const checked = value === true;
  const unknown = value === undefined;
  const disabled = !sender.canSend;
  const word = checked ? sender.strings.on : sender.strings.off;

  const chars = useStickyChars(word.length);
  const floor = Math.round(FIT_MIN_FONT_PX * ws.scale);
  const measured = ws.size.width > 0 && ws.size.height > 0;
  const room = {
    width: Math.floor(ws.size.width - TRACK_W - 16),
    height: Math.max(TOUCH_TARGET_PX, Math.floor(ws.size.height * 0.5)),
  };
  const font = measured ? fitValueFont(room, chars, { floor }) : fitValueFontCss(chars, { floor });

  const sendValue = (next: boolean) => {
    sender.send(payloadFor(binding, next), next);
  };

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
      {unknown ? (
        <div role="group" aria-label={label} aria-describedby={sender.readOnlyReason ? `${captionId} ${readOnlyId(captionId)}` : captionId} data-toggle-choose="" style={{ display: 'flex', gap: spaceVar(2) }}>
          {([true, false] as const).map((v) => (
            <button
              key={String(v)}
              type="button"
              disabled={disabled}
              aria-busy={sender.pending}
              onClick={() => sendValue(v)}
              style={{
                flex: '1 1 0',
                minHeight: TOUCH_TARGET_PX,
                minWidth: TOUCH_TARGET_PX,
                padding: `0 ${spaceVar(2)}`,
                borderRadius: 6,
                border: `1px solid ${css('border')}`,
                background: css('background'),
                color: css('foreground'),
                fontWeight: 600,
                cursor: disabled ? 'not-allowed' : 'pointer',
                opacity: disabled ? 0.6 : 1,
              }}
            >
              {v ? sender.strings.on : sender.strings.off}
            </button>
          ))}
        </div>
      ) : (
        <button
          type="button"
          role="switch"
          aria-checked={checked}
          aria-label={label}
          aria-describedby={sender.readOnlyReason ? `${captionId} ${readOnlyId(captionId)}` : captionId}
          aria-busy={sender.pending}
          disabled={disabled}
          onClick={() => sendValue(!checked)}
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: spaceVar(2),
            minHeight: TOUCH_TARGET_PX,
            minWidth: TOUCH_TARGET_PX,
            padding: 0,
            border: 'none',
            background: 'transparent',
            color: css('foreground'),
            cursor: disabled ? 'not-allowed' : 'pointer',
            opacity: disabled ? 0.6 : 1,
            textAlign: 'left',
          }}
        >
          <span
            aria-hidden="true"
            style={{
              position: 'relative',
              flex: '0 0 auto',
              width: TRACK_W,
              height: TRACK_H,
              borderRadius: TRACK_H / 2,
              boxSizing: 'border-box',
              border: `1px solid ${css('border')}`,
              background: checked ? css('primary') : css('muted'),
            }}
          >
            <span
              style={{
                position: 'absolute',
                top: (TRACK_H - THUMB) / 2 - 1,
                left: checked ? TRACK_W - THUMB - 3 : 2,
                width: THUMB,
                height: THUMB,
                borderRadius: '50%',
                background: checked ? css('primary-foreground') : css('background'),
                border: `1px solid ${css('border')}`,
                boxSizing: 'border-box',
              }}
            />
          </span>
          <span
            data-toggle-word=""
            style={{
              fontSize: font,
              fontWeight: 600,
              lineHeight: 1.1,
              whiteSpace: 'nowrap',
              color: css('foreground'),
            }}
          >
            {word}
          </span>
        </button>
      )}
      {/* One row, so the short read-only caption costs no height at a two-row widget's minimum span. */}
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'baseline', columnGap: spaceVar(2) }}>
        {caption}
        <ControlNotes sender={sender} captionId={captionId} />
      </div>
    </div>
  );
}
