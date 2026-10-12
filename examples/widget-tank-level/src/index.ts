// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { tankLevelCardWidget } from './tank-level-card';
import { tankLevelGaugeWidget } from './tank-level-gauge';

export { tankLevelCardWidget, tankLevelGaugeWidget };

// Every widget this package provides. A host adds them to its registry explicitly:
//
//   createWidgetRegistry([...builtinWidgetDefinitions, ...tankLevelWidgets])
export const tankLevelWidgets = [tankLevelCardWidget, tankLevelGaugeWidget] as const;
