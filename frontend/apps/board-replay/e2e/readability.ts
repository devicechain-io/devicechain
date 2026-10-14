// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The readability measurement, run INSIDE the page (page.evaluate serialises the function, so
// it may not close over anything outside its own body).
//
// What fails:
//
//   text-overflow  a text node's container (its nearest element with a box of its own) holds
//                  more than its box: scrollWidth > clientWidth, or scrollHeight >
//                  clientHeight on an axis that does not scroll. An ellipsis counts: truncated
//                  text is text the reader cannot read.
//   text-clipped   a text node's glyph box lies outside an ancestor that clips it, or outside
//                  its widget's box, which a card clips and a neighbour sits beside. The frame
//                  body clips by design, so content that outgrows it no longer overflows
//                  anything; this is how it is still caught. Horizontally, a SCROLL container
//                  clips too: a column cut off at the right is unread on a wall display, where
//                  nobody scrolls sideways.
//   overlap        two widgets' boxes intersect.
//
// What does not:
//
//   - text beyond a box that scrolls VERTICALLY: a list or table that scrolls down is
//     reachable, and is how a long list is meant to behave;
//   - text with no box (display none, an unopened <option>) or a box of at most 1x1 px (the
//     screen-reader-only pattern), and text under visibility hidden;
//   - a vertical excess within the face's own LEADING. A text box is the face's whole
//     ascent-to-descent, about 1.2 em in Inter, while a readout set at `line-height: 1` (or in
//     a box sized to its em) is one em tall. The difference holds no ink of the digits and
//     capitals a board prints, so a container may be over by the leading, and a clipping edge
//     may cut half of it at either side. The leading is measured from the text's own line box,
//     not assumed.
//
// Every finding names its widget and the widget's box, in page coordinates.

export type FindingKind = 'text-overflow' | 'text-clipped' | 'overlap';

export interface Box {
  x: number;
  y: number;
  w: number;
  h: number;
}

export interface Finding {
  widget: string;
  kind: FindingKind;
  box: Box;
  // For an overlap: the other widget.
  other?: string;
  detail: string;
}

export interface Measurement {
  widgets: string[];
  findings: Finding[];
}

export function measureReadability(): Measurement {
  const TOLERANCE_PX = 1;

  const pageBox = (r: DOMRect) => ({
    x: Math.round(r.left + window.scrollX),
    y: Math.round(r.top + window.scrollY),
    w: Math.round(r.width),
    h: Math.round(r.height),
  });
  const clips = (v: string) => v === 'hidden' || v === 'clip';
  const scrolls = (v: string) => v === 'auto' || v === 'scroll';
  const snippet = (s: string) => {
    const t = s.replace(/\s+/g, ' ').trim();
    return t.length > 40 ? `${t.slice(0, 40)}...` : t;
  };

  const widgets = Array.from(document.querySelectorAll<HTMLElement>('[data-widget]'));
  const findings: Finding[] = [];
  const rects = widgets.map((w) => w.getBoundingClientRect());

  for (let i = 0; i < widgets.length; i++) {
    for (let j = i + 1; j < widgets.length; j++) {
      const a = rects[i];
      const b = rects[j];
      const ix = Math.min(a.right, b.right) - Math.max(a.left, b.left);
      const iy = Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top);
      if (ix > TOLERANCE_PX && iy > TOLERANCE_PX) {
        findings.push({
          widget: widgets[i].dataset.widget!,
          other: widgets[j].dataset.widget!,
          kind: 'overlap',
          box: pageBox(a),
          detail: `overlaps ${widgets[j].dataset.widget} ${JSON.stringify(pageBox(b))} by ${Math.round(ix)}x${Math.round(iy)} px`,
        });
      }
    }
  }

  for (let i = 0; i < widgets.length; i++) {
    const widget = widgets[i];
    const id = widget.dataset.widget!;
    const box = pageBox(rects[i]);
    const seen = new Set<string>();
    // One finding per (kind, subject): the innermost edge that cuts a text is the one named.
    const report = (kind: FindingKind, subject: string, detail: string) => {
      if (seen.has(`${kind}|${subject}`)) return;
      seen.add(`${kind}|${subject}`);
      findings.push({ widget: id, kind, box, detail });
    };
    const checkedContainers = new Set<Element>();
    const walker = document.createTreeWalker(widget, NodeFilter.SHOW_TEXT);
    for (let node = walker.nextNode(); node; node = walker.nextNode()) {
      const text = node.textContent ?? '';
      if (text.trim() === '') continue;
      const parent = node.parentElement;
      if (!parent) continue;
      const style = getComputedStyle(parent);
      if (style.visibility !== 'visible') continue;
      const range = document.createRange();
      range.selectNodeContents(node);
      const lines = range.getClientRects();
      if (lines.length === 0) continue;
      const glyphs = range.getBoundingClientRect();
      if (glyphs.width <= 1 && glyphs.height <= 1) continue;
      // The face's leading at this size: one line's text box less the em.
      const leading = Math.max(0, lines[0].height - parseFloat(style.fontSize));

      // The container: the nearest element with a box of its own.
      let container: HTMLElement = parent;
      while (container !== widget && ['inline', 'contents'].includes(getComputedStyle(container).display)) {
        container = container.parentElement!;
      }
      if (container.clientWidth * container.clientHeight > 1 && !checkedContainers.has(container)) {
        checkedContainers.add(container);
        const overX = container.scrollWidth > container.clientWidth + TOLERANCE_PX;
        const overY =
          !scrolls(getComputedStyle(container).overflowY) &&
          container.scrollHeight > container.clientHeight + Math.max(TOLERANCE_PX, leading);
        if (overX || overY) {
          report(
            'text-overflow',
            snippet(container.textContent ?? ''),
            `"${snippet(container.textContent ?? '')}" needs ${container.scrollWidth}x${container.scrollHeight} px, ` +
              `has ${container.clientWidth}x${container.clientHeight}`,
          );
        }
      }

      // Clipping: every clipping ancestor up to the widget, and the widget's own box. The
      // vertical check stops at the first ancestor that scrolls vertically.
      const slackY = Math.max(TOLERANCE_PX, leading / 2);
      let checkY = true;
      for (let el: HTMLElement | null = parent; el; el = el.parentElement) {
        const s = getComputedStyle(el);
        const isWidget = el === widget;
        const r = el.getBoundingClientRect();
        // The padding box: the border box less its borders (and less a scrollbar).
        const left = r.left + el.clientLeft;
        const top = r.top + el.clientTop;
        const right = left + (isWidget ? r.width : el.clientWidth);
        const bottom = top + (isWidget ? r.height : el.clientHeight);
        const where = isWidget ? 'the widget' : `<${el.tagName.toLowerCase()}>`;
        if (isWidget || clips(s.overflowX) || scrolls(s.overflowX)) {
          if (glyphs.left < left - TOLERANCE_PX) report('text-clipped', `${snippet(text)}|left`, `"${snippet(text)}" runs left of ${where}`);
          if (glyphs.right > right + TOLERANCE_PX) report('text-clipped', `${snippet(text)}|right`, `"${snippet(text)}" runs right of ${where}`);
        }
        if (checkY && (isWidget || clips(s.overflowY))) {
          if (glyphs.top < top - slackY) report('text-clipped', `${snippet(text)}|above`, `"${snippet(text)}" runs above ${where}`);
          if (glyphs.bottom > bottom + slackY) report('text-clipped', `${snippet(text)}|below`, `"${snippet(text)}" runs below ${where}`);
        }
        if (scrolls(s.overflowY)) checkY = false;
        if (isWidget) break;
      }
    }
  }

  return { widgets: widgets.map((w) => w.dataset.widget!), findings };
}

// A signature of everything the measurement reads, for the settle wait: when it has not
// changed for a run of frames, the board has stopped moving. It covers each widget's box, its
// text, and the box of every element that holds text directly (a readout that re-sizes its
// font after measuring itself changes only the last).
export function layoutSignature(): string {
  const parts: string[] = [];
  for (const w of Array.from(document.querySelectorAll<HTMLElement>('[data-widget]'))) {
    const r = w.getBoundingClientRect();
    parts.push(`${w.dataset.widget}:${r.left},${r.top},${r.width},${r.height}:${w.textContent ?? ''}`);
    for (const el of Array.from(w.querySelectorAll<HTMLElement>('*'))) {
      if (!Array.from(el.childNodes).some((n) => n.nodeType === Node.TEXT_NODE && (n.textContent ?? '').trim() !== '')) continue;
      const e = el.getBoundingClientRect();
      parts.push(`${e.left},${e.top},${e.width},${e.height},${el.scrollWidth},${el.scrollHeight}`);
    }
  }
  return parts.join('|');
}
