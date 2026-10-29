// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The English `dashboards` catalog, registered when the first module that uses it loads rather than
// bundled into the first download (see i18n/lazyEnglish.ts). Import this module, for its side
// effect, from every module that uses the namespace.

import { registerEnglish } from '@/i18n/lazyEnglish';
import en from '@/i18n/locales/en/dashboards.json';

registerEnglish('dashboards', en);
