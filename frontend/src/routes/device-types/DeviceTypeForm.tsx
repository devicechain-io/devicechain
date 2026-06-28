// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import {
  createDeviceType,
  updateDeviceType,
  type DeviceType,
} from '@/lib/api/device-management';
import { Textarea, errMessage } from '@/routes/common';

// DeviceTypeForm creates a device type (deviceType absent) or edits one
// (deviceType present, with its token fixed). Shared by the new + detail pages.
export function DeviceTypeForm({
  deviceType,
  onDone,
}: {
  deviceType?: DeviceType;
  onDone: (message: string) => void;
}) {
  const editing = deviceType != null;
  const [token, setToken] = useState(deviceType?.token ?? '');
  const [name, setName] = useState(deviceType?.name ?? '');
  const [description, setDescription] = useState(deviceType?.description ?? '');
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      if (editing) {
        await updateDeviceType(deviceType.token, {
          token: deviceType.token,
          name: name.trim() || undefined,
          description: description.trim() || undefined,
        });
        onDone(`Device type “${deviceType.token}” updated`);
      } else {
        const t = token.trim();
        await createDeviceType({
          token: t,
          name: name.trim() || undefined,
          description: description.trim() || undefined,
        });
        onDone(`Device type “${t}” created`);
      }
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
        label="Token"
        htmlFor="dt-token"
        description={editing ? undefined : 'Unique id for this device type; it cannot change later.'}
      >
        <Input
          id="dt-token"
          value={token}
          disabled={editing}
          placeholder="thermostat"
          onChange={(e) => setToken(e.target.value)}
        />
      </FormField>
      <FormField label="Name" htmlFor="dt-name">
        <Input
          id="dt-name"
          value={name}
          placeholder="Thermostat"
          onChange={(e) => setName(e.target.value)}
        />
      </FormField>
      <FormField label="Description" htmlFor="dt-description">
        <Textarea
          id="dt-description"
          value={description}
          placeholder="Smart thermostat device type"
          onChange={(e) => setDescription(e.target.value)}
        />
      </FormField>
      <div className="flex gap-2">
        <Button onClick={submit} loading={busy} disabled={busy || (!editing && !token.trim())}>
          {editing ? 'Save changes' : 'Create device type'}
        </Button>
      </div>
    </div>
  );
}
