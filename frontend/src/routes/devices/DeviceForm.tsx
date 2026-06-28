// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { useQuery } from '@/lib/hooks/use-query';
import {
  createDevice,
  updateDevice,
  listDeviceTypes,
  type Device,
} from '@/lib/api/device-management';
import { Textarea, errMessage } from '@/routes/common';

// DeviceForm creates a device (device absent) or edits one (device present, with
// its token fixed). Shared by the new + detail pages.
export function DeviceForm({
  device,
  onDone,
}: {
  device?: Device;
  onDone: (message: string) => void;
}) {
  const editing = device != null;
  const [token, setToken] = useState(device?.token ?? '');
  const [name, setName] = useState(device?.name ?? '');
  const [description, setDescription] = useState(device?.description ?? '');
  const [deviceTypeToken, setDeviceTypeToken] = useState(device?.deviceType.token ?? '');
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // Device types power the type selector below.
  const { data: deviceTypes } = useQuery(
    () => listDeviceTypes({ pageNumber: 1, pageSize: 1000 }),
    [],
  );
  const options: ComboboxOption[] = (deviceTypes?.results ?? []).map((dt) => ({
    value: dt.token,
    label: dt.name || dt.token,
    description: dt.name ? dt.token : undefined,
  }));
  const noDeviceTypes = deviceTypes != null && options.length === 0;

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      if (editing) {
        await updateDevice(device.token, {
          token: device.token,
          name: name.trim() || undefined,
          description: description.trim() || undefined,
          deviceTypeToken,
        });
        onDone(`Device “${device.token}” updated`);
      } else {
        const t = token.trim();
        await createDevice({
          token: t,
          name: name.trim() || undefined,
          description: description.trim() || undefined,
          deviceTypeToken,
        });
        onDone(`Device “${t}” created`);
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
        htmlFor="d-token"
        description={editing ? undefined : 'Unique id for this device; it cannot change later.'}
      >
        <Input
          id="d-token"
          value={token}
          disabled={editing}
          placeholder="sensor-001"
          onChange={(e) => setToken(e.target.value)}
        />
      </FormField>
      <FormField label="Name" htmlFor="d-name">
        <Input
          id="d-name"
          value={name}
          placeholder="Lobby sensor"
          onChange={(e) => setName(e.target.value)}
        />
      </FormField>
      <FormField label="Description" htmlFor="d-description">
        <Textarea
          id="d-description"
          value={description}
          placeholder="Temperature sensor in the lobby"
          onChange={(e) => setDescription(e.target.value)}
        />
      </FormField>
      <FormField
        label="Device type"
        htmlFor="d-device-type"
        description={noDeviceTypes ? 'Create a device type first.' : undefined}
      >
        <Combobox
          id="d-device-type"
          value={deviceTypeToken}
          onChange={setDeviceTypeToken}
          options={options}
          placeholder="Select a device type…"
          disabled={noDeviceTypes}
        />
      </FormField>
      <div className="flex gap-2">
        <Button
          onClick={submit}
          loading={busy}
          disabled={busy || noDeviceTypes || (!editing && !token.trim()) || !deviceTypeToken}
        >
          {editing ? 'Save changes' : 'Create device'}
        </Button>
      </div>
    </div>
  );
}
