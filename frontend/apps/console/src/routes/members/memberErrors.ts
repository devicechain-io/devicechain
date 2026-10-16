// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { TFunction } from 'i18next';
import { errorCode } from '@devicechain/client';
import { errMessage } from '@/routes/common';

// The refusals the member and role administration answers with a code of its own, mapped
// to copy that says what to do about each. The code is the contract; the server's message
// is the fallback for anything not listed here, so an unknown refusal still says
// something true rather than nothing.
const CODE_KEYS: Record<string, string> = {
  ESCALATION: 'members:errorEscalation',
  SELF_MODIFICATION: 'members:errorSelfModification',
  LAST_ADMIN: 'members:errorLastAdmin',
  MANAGED_BY_IDP: 'members:errorManagedByIdp',
  READ_ONLY: 'members:errorReadOnly',
  CONFLICT: 'members:errorConflict',
  LIMIT_EXCEEDED: 'members:errorLimitExceeded',
  REFERENCE_VIOLATION: 'members:errorRoleInUse',
  INVALID_INVITATION: 'members:errorInvalidInvitation',
  SSO_REQUIRED: 'members:errorSsoRequired',
};

/**
 * The message to show for a failed member, invitation or role operation. `overrides` maps
 * a code to a key where the same code means something narrower on one screen (CONFLICT is
 * "already a member" on an invitation and "token taken" on a role).
 */
export function memberErrorMessage(err: unknown, t: TFunction, overrides: Record<string, string> = {}): string {
  const code = errorCode(err);
  const key = code ? (overrides[code] ?? CODE_KEYS[code]) : undefined;
  return key ? t(key) : errMessage(err);
}
