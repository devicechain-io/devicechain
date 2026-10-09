// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The "never live" scanner shared by the catalog test and the rendered-DOM test. A replay
// must not describe itself, in any language, as live: English "live", Spanish "en vivo",
// and the Chinese words for real-time / live broadcast.

export const LIVE_PATTERNS: RegExp[] = [/\blive\b/i, /\ben vivo\b/i, /实时|直播/];

export function liveMatches(text: string): string[] {
  return LIVE_PATTERNS.filter((re) => re.test(text)).map(String);
}

// Everything a person could read or hear from an element tree: its text plus the
// attributes that carry words.
export function readableStrings(root: ParentNode): string[] {
  const out: string[] = [];
  const walker = document.createTreeWalker(root as Node, NodeFilter.SHOW_ELEMENT | NodeFilter.SHOW_TEXT);
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    if (n.nodeType === Node.TEXT_NODE) {
      if (n.textContent?.trim()) out.push(n.textContent);
      continue;
    }
    const el = n as Element;
    for (const attr of ['aria-label', 'aria-valuetext', 'title', 'placeholder', 'alt', 'value']) {
      const v = el.getAttribute(attr);
      if (v) out.push(v);
    }
  }
  return out;
}
