// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// THE ONE PLACE AN OPERATOR ADDS WIDGET PACKAGES TO THIS DEPLOYMENT.
//
// The console, the /dash viewer and the board-replay player all build their widget registry
// from the list below (see ./index.ts) -- none of them keeps a list of its own -- so a
// package added here is available to the parser, the validators, the renderer, the editor's
// Add menu and settings panel, the synthetic preview and the recording player at once.
//
// To install a widget package:
//   1. add it to this package's dependencies (apps/host-widgets/package.json) and run
//      `npm install` in frontend/;
//   2. import its definitions below and list them in `extensionWidgets`;
//   3. rebuild the apps (`npm run build` in frontend/) and redeploy ALL of them. A board
//      authored in the console renders in /dash only if /dash was built with the same
//      package, and a viewer without it shows a labelled placeholder rather than the widget.
//
//   import { tankLevelWidget } from '@acme/devicechain-widgets';
//   export const extensionWidgets = [tankLevelWidget];
//
// This is a build-time list of TRUSTED code: a widget runs in the page with the page's
// privileges. It is not a runtime installer, and a dashboard cannot add to it.

import type { ErasedWidgetDefinition } from '@devicechain/widget-sdk';

export const extensionWidgets: readonly ErasedWidgetDefinition[] = [];
