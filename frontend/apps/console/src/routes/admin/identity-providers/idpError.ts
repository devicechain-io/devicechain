// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { TFunction } from 'i18next';
import { GraphQLRequestError } from '@devicechain/client';
import { serverErrorCode } from '@/i18n/signInError';
import { errMessage } from '@/routes/common';
import '@/i18n/lazy/identityProviders';

// The server refuses a disable, a delete, or a dropped tenant that would leave a tenant
// requiring single sign-on with no enabled connection serving it, by extensions.code, with
// the affected tenants in extensions.tenants. Matched on the code, never on the prose.
const STRANDED = 'SSO_ENFORCED_TENANT_STRANDED';

function strandedTenants(err: unknown): string[] {
  if (!(err instanceof GraphQLRequestError)) return [];
  const tenants = err.errors?.[0]?.extensions?.tenants;
  return Array.isArray(tenants) ? tenants.filter((x): x is string => typeof x === 'string') : [];
}

/**
 * What to show for a failed connection write: the stranding refusal in the operator's
 * language, naming the tenants and the fix; anything else (an unknown code, or a stranding
 * that names no tenants) as the server's own message.
 */
export function idpErrorMessage(err: unknown, t: TFunction): string {
  if (serverErrorCode(err) === STRANDED) {
    const tenants = strandedTenants(err);
    if (tenants.length > 0) {
      return t('identityProviders:strandedRefusal', { tenants: tenants.join(', ') });
    }
  }
  return errMessage(err);
}
