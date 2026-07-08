// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// entity-selector — a flat picker that re-points a target slot's binding (ADR-039 selection
// amendment). It is the authored companion to the alarm-originator drill: the viewer chooses
// WHICH entity a slot binds to, and every widget bound to that slot (and its scoped children,
// via the cascade) follows. Two shapes, both from `options.selectionTarget`:
//   • target is a SCOPED child slot → a member picker (which thermostat within the building).
//   • target is a ROOT slot          → a context picker (which building the dashboard shows).
// The candidate set comes from the ambient provider (useCandidates), which the host computes
// from the target slot's scope; the pick fires the ambient select() callback. Inert (a muted
// hint) wherever selection isn't wired — edit mode, preview, a read-only embedder.

import type { SlotBinding } from '@devicechain/dashboards';
import { useEffect, useState, type ChangeEvent, type CSSProperties } from 'react';

import { WidgetFrame, useWidgetSelect } from '../frame';
import { useCandidates } from '../hooks';
import { css } from '../theme';
import { optString, type WidgetProps } from '../widget';

// A binding's stable option value — the device token or the anchor's target token. Used as
// the <option> value (not the array index) so the control's value survives a re-fetch that
// reorders/replaces candidates.
function tokenOf(b: SlotBinding): string {
  return b.kind === 'device' ? b.deviceToken : b.anchor.targetToken;
}

const control: CSSProperties = {
  width: '100%',
  fontSize: 14,
  padding: '8px 10px',
  borderRadius: 6,
  border: `1px solid ${css('border')}`,
  background: css('background'),
  color: css('foreground'),
};

const hint: CSSProperties = {
  fontSize: 12,
  color: css('muted-foreground'),
};

export function EntitySelector({ widget }: WidgetProps<null>) {
  const select = useWidgetSelect();
  const target = optString(widget.options, 'selectionTarget');
  const { candidates, loading, error, wired } = useCandidates(target);
  const title = optString(widget.options, 'title');

  // Optimistic pick: hold the just-chosen token so the control reflects the choice
  // immediately — through the (on a scoped dashboard, two-hop) async re-resolve — rather
  // than snapping back to the prior selection until the refetch settles. Cleared once the
  // fetched candidates reflect it, or it is no longer offered (the pick was dropped).
  const [pending, setPending] = useState<string | null>(null);
  const selected = candidates.find((c) => c.selected);
  const selectedToken = selected ? tokenOf(selected.binding) : '';
  useEffect(() => {
    if (pending !== null && (selectedToken === pending || !candidates.some((c) => tokenOf(c.binding) === pending))) {
      setPending(null);
    }
  }, [candidates, selectedToken, pending]);

  // Inert wherever selection isn't wired (no select callback OR no candidate provider) or no
  // target slot is authored — the picker would drive nothing, so show a muted explanation.
  const inert = !select || !target || !wired;
  const value = pending ?? selectedToken;

  const onChange = (e: ChangeEvent<HTMLSelectElement>) => {
    const token = e.target.value;
    if (!token) return; // the disabled placeholder ('') — nothing to select
    const candidate = candidates.find((c) => tokenOf(c.binding) === token);
    if (select && target && candidate) {
      setPending(token);
      select({ slot: target, binding: candidate.binding });
    }
  };

  return (
    <WidgetFrame title={title}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8, padding: '12px 16px', height: '100%' }}>
        {inert ? (
          <span style={hint}>
            {select && target ? 'Selection isn’t available here.' : select ? 'No target slot configured.' : 'Selection is available on the live dashboard.'}
          </span>
        ) : (
          <>
            <select
              style={{ ...control, opacity: loading ? 0.6 : 1 }}
              value={value}
              disabled={candidates.length === 0}
              onChange={onChange}
              aria-label={title || `Select ${target}`}
            >
              {/* A blank option so an unbound slot (no member picked yet, or the prior pick
                  is out of the new context) shows a prompt rather than defaulting to a row. */}
              <option value="" disabled>
                {loading ? 'Loading…' : candidates.length === 0 ? 'No options' : 'Select…'}
              </option>
              {candidates.map((c) => (
                <option key={tokenOf(c.binding)} value={tokenOf(c.binding)}>
                  {c.label}
                </option>
              ))}
            </select>
            {error ? <span style={hint}>Couldn’t load options.</span> : null}
          </>
        )}
      </div>
    </WidgetFrame>
  );
}
