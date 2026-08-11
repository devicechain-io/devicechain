// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Driving the kit's Select from a test.
//
// 🔴 IT IS NO LONGER A NATIVE `<select>`, so `fireEvent.change(el, {target:{value}})`
// does nothing at all — silently, because a change event on a button is perfectly
// legal and simply ignored. Every test that drove the old native element that way
// kept passing its render assertions while asserting nothing about selection.
//
// Radix opens on POINTERDOWN (not click), and renders its items into a portal with
// role="option". This helper does the real sequence, so a test exercises the same
// path a user does rather than a synthetic shortcut.

import { fireEvent, screen, within } from '@testing-library/react';

/** Open a Select by its trigger and choose the item with the given visible text. */
export async function selectOption(trigger: HTMLElement, optionText: string | RegExp) {
  fireEvent.pointerDown(trigger, { button: 0, ctrlKey: false, pointerType: 'mouse' });
  const option = await screen.findByRole('option', { name: optionText });
  fireEvent.click(option);
}

/** The text a Select's trigger is currently displaying — its selected label. */
export function selectedLabel(trigger: HTMLElement): string {
  return trigger.textContent?.trim() ?? '';
}

/** The visible labels a Select is currently offering. Trigger must already be open. */
export function openOptions(): string[] {
  return screen.queryAllByRole('option').map((o) => o.textContent?.trim() ?? '');
}

export { within };
