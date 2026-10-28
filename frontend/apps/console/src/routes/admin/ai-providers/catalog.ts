// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The English `aiProviders` catalog, registered when the AI Providers admin screens load rather
// than bundled into the first download (see i18n/lazyEnglish.ts). Import this module, for its side
// effect, from every module that uses the namespace.

import { registerEnglish } from '@/i18n/lazyEnglish';
import en from '@/i18n/locales/en/aiProviders.json';

registerEnglish('aiProviders', en);
