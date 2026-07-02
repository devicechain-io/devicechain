// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';

// useNoAutofill suppresses password-manager autofill/save on fields that are not
// the operator's own login — e.g. device credentials being registered on another
// entity's behalf. Spread the returned `noAutofill` onto each Input.
//
// It opts out of the major managers via their data-* flags (1Password/LastPass/
// Bitwarden), and — because LastPass ignores those on login-shaped fields — also
// starts the field read-only and unlocks it on focus, which reliably suppresses
// page-load autofill across every manager. Call `rearm()` whenever the fields are
// re-presented empty (a type switch, or after a successful submit) so the guard
// re-engages for the now-blank inputs instead of staying unlocked from a prior
// focus.
export function useNoAutofill() {
  const [interacted, setInteracted] = useState(false);
  const noAutofill = {
    autoComplete: 'off',
    readOnly: !interacted,
    onFocus: () => setInteracted(true),
    'data-1p-ignore': 'true',
    'data-lpignore': 'true',
    'data-bwignore': 'true',
    'data-form-type': 'other',
  } as const;
  return { noAutofill, rearm: () => setInteracted(false) };
}
