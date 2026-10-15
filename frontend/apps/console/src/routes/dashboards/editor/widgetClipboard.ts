// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The tab's own widget clipboard: the last widget copied in this tab, as clipboard text. It is what
// the Paste button reads (reading the system clipboard needs a permission prompt), and it outlives
// moving from one dashboard to another, so a widget copied on one board pastes onto the next.
// Mirrored into sessionStorage so a reload of the tab keeps it; every storage access is guarded,
// because storage can be missing or refuse (a private window, blocked site data).

const KEY = 'devicechain.dashboard.widget-clipboard';

let memory: string | undefined;

export function readWidgetClipboard(): string | undefined {
  if (memory !== undefined) return memory;
  try {
    return sessionStorage.getItem(KEY) ?? undefined;
  } catch {
    return undefined;
  }
}

export function writeWidgetClipboard(text: string): void {
  memory = text;
  try {
    sessionStorage.setItem(KEY, text);
  } catch {
    // The in-memory copy still serves this tab.
  }
}

// For tests: forget what was copied.
export function clearWidgetClipboard(): void {
  memory = undefined;
  try {
    sessionStorage.removeItem(KEY);
  } catch {
    // nothing to clear
  }
}
