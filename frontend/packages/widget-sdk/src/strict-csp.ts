// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Strict-CSP host mode: run the SDK's widgets on a page whose content-security policy has no
// `style-src 'unsafe-inline'` (for example `style-src 'self'`).
//
// OFF BY DEFAULT, and the default behaviour is unchanged: HTML chart tooltips and a <style>
// element for the token CSS. A host that serves under a strict policy calls
// `configureStrictCsp(true)` once, before it mounts anything:
//
//   - chart tooltips are drawn on the canvas (ECharts `renderMode: 'richText'`) instead of as HTML,
//     because the HTML tooltip writes `style="..."` attributes into its markup and a strict
//     policy refuses those (the rows lose their spacing and colour keys). Consequences: a custom
//     formatter must return plain text, not HTML; `{name|text}` in a string is zrender rich-text
//     syntax, so a `{` or `}` in a series or event name can truncate that tooltip line; and the
//     tooltip's `extraCssText`, `className` and `appendTo` options have no effect.
//   - the token CSS is adopted by the document as a constructed stylesheet (CSSOM, which
//     `style-src` does not govern) rather than rendered as a <style> element. It is adopted on
//     `document`, so a board hosted inside a shadow root does not receive it.
//
// A browser without constructed stylesheets (Safari before 16.4) falls back to the element.

let strict = false;

export function configureStrictCsp(enabled: boolean): void {
  strict = enabled;
}

export function strictCspEnabled(): boolean {
  return strict;
}

// A string the canvas tooltip will show as written: `{` and `}` are zrender rich-text delimiters,
// so they become their full-width forms rather than starting or ending a style run.
export function plainTooltipText(text: string): string {
  return text.replace(/\{/g, '\uFF5B').replace(/\}/g, '\uFF5D');
}
