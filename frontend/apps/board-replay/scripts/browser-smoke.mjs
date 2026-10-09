// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Browser smoke for the replay page: NOT run in CI and NOT implemented yet.
//
// This workspace has no browser-automation dependency (no Playwright or Puppeteer in the
// lockfile), and a headless browser is too heavy to add for one script. The jsdom suite
// covers labels, languages, controls, the golden render and the network trap; what only a
// real browser can show is covered by this checklist, to be run before each website sync.
//
// What the script must do when it is written (against the BUILT output, served by a static
// server that sends the website's exact response headers, content-security policy included):
//   1. Load the page and wait for the transport bar.
//   2. Listen for `securitypolicyviolation` events and for every network request; fail on
//      any violation and on any request whose origin is not the page's own.
//   3. Press Play, wait a few seconds, seek to the tyre-alarm chapter, and check the board
//      shows the alarm row; drill from the row and check the Machine selector follows.
//   4. Check the map card says "Not in this recording" while the recording carries no
//      positions, and that a recording WITH positions draws a map (worker loaded same-origin).
//   5. Repeat for the en, es and zh-CN pages.
//
// Until then this script fails loudly rather than reporting a pass it did not earn.

console.error('browser-smoke: not implemented (no browser-automation dependency in this workspace).');
console.error('Run the checklist in this file by hand before each website sync.');
process.exit(2);
