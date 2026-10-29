// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// "Add LwM2M standard commands": declares the adapter's named Device-object operations
// (reboot, read device information) on a profile as ordinary command definitions, so they
// appear in the device command picker and pass the same enqueue check, permissions and
// audit trail as every other command. It adds only what is missing, so pressing it twice
// (or on a profile that already declares one by hand) creates no duplicate and changes
// nothing that exists.
//
// Factory Reset is deliberately not here: it is destructive and has no confirmation flow
// yet. The keys below must equal the adapter's named command keys.

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Plus } from 'lucide-react';
import { hasAuthority } from '@devicechain/client';
import { Button } from '@/components/ui/button';
import { useToast } from '@/components/ui/toast';
import { useAuth } from '@/auth/AuthProvider';
import { errMessage } from '@/routes/common';
import { createCommandDefinition, listCommandDefinitions } from '@/lib/api/device-management';
import '@/i18n/lazy/deviceProfiles';

// The name and description are stored on the profile, a shared tenant resource, so they are the
// canonical (English) text rather than the language of whoever pressed the button: the same
// profile must not read differently depending on who declared its commands. They can be edited
// afterwards like any other command definition.
export const LWM2M_STANDARD_COMMANDS = [
  {
    commandKey: 'lwm2m.reboot',
    slug: 'lwm2m-reboot',
    name: 'Reboot',
    // A reboot interrupts the device, so it waits for the device type's maintenance window.
    impact: 'DISRUPTIVE',
    description: 'Reboots the device (LwM2M Device object, Reboot resource).',
  },
  {
    commandKey: 'lwm2m.readDeviceInfo',
    slug: 'lwm2m-read-device-info',
    name: 'Read device information',
    description:
      'Reads manufacturer, model number, serial number, firmware version and battery level, and reports any the device did not provide.',
    impact: 'NORMAL',
  },
] as const;

export function Lwm2mStandardCommands({
  profileToken,
  onAdded,
}: {
  profileToken: string;
  onAdded: () => void;
}) {
  const { t } = useTranslation('deviceProfiles');
  const { toast } = useToast();
  const { claims } = useAuth();
  const [busy, setBusy] = useState(false);

  if (!hasAuthority(claims, 'device:write')) return null;

  const add = async () => {
    setBusy(true);
    try {
      const existing = new Set((await listCommandDefinitions(profileToken)).map((d) => d.commandKey));
      const missing = LWM2M_STANDARD_COMMANDS.filter((c) => !existing.has(c.commandKey));
      for (const c of missing) {
        await createCommandDefinition({
          token: `${profileToken}-${c.slug}`,
          deviceProfileToken: profileToken,
          commandKey: c.commandKey,
          name: c.name,
          description: c.description,
          parameterSchema: null,
          impact: c.impact,
          metadata: null,
        });
      }
      toast(
        missing.length === 0
          ? t('lwm2mCommandsAlreadyDeclared')
          : t('lwm2mCommandsAdded', { count: missing.length }),
      );
      if (missing.length > 0) onAdded();
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex items-start justify-between gap-4 rounded-md border border-dashed p-3">
      <p className="max-w-prose text-sm text-muted-foreground">{t('lwm2mCommandsDescription')}</p>
      <Button size="sm" variant="outline" onClick={add} loading={busy} disabled={busy} className="shrink-0">
        <Plus size={16} /> {t('lwm2mCommandsAddButton')}
      </Button>
    </div>
  );
}
