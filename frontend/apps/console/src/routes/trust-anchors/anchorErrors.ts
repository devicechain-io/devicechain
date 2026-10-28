// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// An X.509 refusal as the sentence the operator reads. The server's `extensions.code` picks the
// explanation; for the families where the specifics are the useful part (which field is wrong,
// which count was hit) the server's own sentence is appended, so nothing it knew is lost.

import '@/routes/trust-anchors/catalog';
import type { TFunction } from 'i18next';
import { errMessage } from '@/routes/common';
import { classifyX509Error } from './anchorRules';

export function x509ErrorText(err: unknown, t: TFunction): string {
  const kind = classifyX509Error(err, errMessage(err));
  switch (kind.key) {
    case 'errInUse':
      return t('trustAnchors:errInUse');
    case 'errLimit':
      return t('trustAnchors:errLimit', { detail: kind.detail });
    case 'errInvalid':
      return t('trustAnchors:errInvalid', { detail: kind.detail });
    case 'errConflict':
      return t('trustAnchors:errConflict');
    case 'errNotFound':
      return t('trustAnchors:errNotFound');
    case 'errOther':
      return kind.detail;
  }
}
