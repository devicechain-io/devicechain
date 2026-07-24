// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
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
  const { t } = useTranslation(['userMenu', 'common']);
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
      // Send both fields as strings (the form always shows both): "" explicitly
      // clears, which the backend distinguishes from an omitted/null field.
      const updated = await updateProfile({
        firstName: firstName.trim(),
        lastName: lastName.trim(),
      });
      applyUser({
        email: updated.email,
        firstName: updated.firstName ?? null,
        lastName: updated.lastName ?? null,
      });
      onDone(t('accountUpdated'));
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-4">
      {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
      <FormField
        label={t('userMenu:accountEmail')}
        htmlFor="p-email"
        description={t('userMenu:accountEmailFixed')}
      >
        <Input id="p-email" value={user?.email ?? ''} disabled />
      </FormField>
      <FormField label={t('userMenu:accountFirstName')} htmlFor="p-first">
        <Input id="p-first" value={firstName} onChange={(e) => setFirstName(e.target.value)} />
      </FormField>
      <FormField label={t('userMenu:accountLastName')} htmlFor="p-last">
        <Input id="p-last" value={lastName} onChange={(e) => setLastName(e.target.value)} />
      </FormField>
      <div className="flex gap-2">
        <Button onClick={submit} loading={busy} disabled={busy}>
          {t('common:saveChanges')}
        </Button>
      </div>
    </div>
  );
}
