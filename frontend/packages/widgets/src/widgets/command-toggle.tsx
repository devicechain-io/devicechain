// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// command-toggle — sends a command carrying ONE boolean parameter, one command per click.
//
// 🔴 THE SWITCH SHOWS THE LAST VALUE SENT, NOT THE DEVICE'S STATE. It rests where the newest
// command left it (read off the command history) and the caption says "Last sent" with that
// command's delivery status. With nothing sent it is drawn unset (thumb mid-track, a dash for the
// word) and exposed as off, since a switch has no third state; a click then sends true.
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

import { bindControl, lastSent, payloadFor, type Binding } from '../command-control-core';
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
            value={typeof last?.value === 'boolean' ? last.value : undefined}
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
  const word = unknown ? '—' : checked ? sender.strings.on : sender.strings.off;

  const chars = useStickyChars(word.length);
  const floor = Math.round(FIT_MIN_FONT_PX * ws.scale);
  const measured = ws.size.width > 0 && ws.size.height > 0;
  const room = {
    width: Math.floor(ws.size.width - TRACK_W - 16),
    height: Math.max(TOUCH_TARGET_PX, Math.floor(ws.size.height * 0.5)),
  };
  const font = measured ? fitValueFont(room, chars, { floor }) : fitValueFontCss(chars, { floor });

  const click = () => {
    // Opposite of what the switch shows; an unset switch is shown off, so it turns on.
    const next = !checked;
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
      <button
        type="button"
        role="switch"
        aria-checked={checked}
        aria-label={label}
        aria-describedby={captionId}
        aria-busy={sender.pending}
        disabled={disabled}
        data-unset={unknown ? 'true' : undefined}
        onClick={click}
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
              left: unknown ? (TRACK_W - THUMB) / 2 - 1 : checked ? TRACK_W - THUMB - 3 : 2,
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
            color: unknown ? css('muted-foreground') : css('foreground'),
          }}
        >
          {word}
        </span>
      </button>
      {caption}
      <ControlNotes sender={sender} />
    </div>
  );
}
