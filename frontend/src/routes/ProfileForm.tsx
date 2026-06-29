// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { useCurrentUser, useApplyCurrentUser } from '@/auth/CurrentUserProvider';
import { updateProfile } from '@/lib/api/user-management';
import { errMessage } from '@/routes/common';

// Self-service profile edit: a user can change their own first/last name. Email
// is the stable identity key, so it's shown read-only. On save we write the
// result straight into the current-user cache, so the name updates everywhere.
export function ProfileForm({ onDone }: { onDone: (message: string) => void }) {
  const user = useCurrentUser();
  const applyUser = useApplyCurrentUser();
  const [firstName, setFirstName] = useState(user?.firstName ?? '');
  const [lastName, setLastName] = useState(user?.lastName ?? '');
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      const updated = await updateProfile({
        firstName: firstName.trim() || null,
        lastName: lastName.trim() || null,
      });
      applyUser({
        email: updated.email,
        firstName: updated.firstName ?? null,
        lastName: updated.lastName ?? null,
      });
      onDone('Profile updated');
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-4">
      {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
      <FormField label="Email" htmlFor="p-email" description="Your sign-in identity; it can't change.">
        <Input id="p-email" value={user?.email ?? ''} disabled />
      </FormField>
      <FormField label="First name" htmlFor="p-first">
        <Input id="p-first" value={firstName} onChange={(e) => setFirstName(e.target.value)} />
      </FormField>
      <FormField label="Last name" htmlFor="p-last">
        <Input id="p-last" value={lastName} onChange={(e) => setLastName(e.target.value)} />
      </FormField>
      <div className="flex gap-2">
        <Button onClick={submit} loading={busy} disabled={busy}>
          Save changes
        </Button>
      </div>
    </div>
  );
}
