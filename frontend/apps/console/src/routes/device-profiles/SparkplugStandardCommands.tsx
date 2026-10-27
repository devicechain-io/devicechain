// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// "Add Sparkplug standard commands": declares the Sparkplug adapter's three command keys
// (write a metric, ask a node to rebirth, reboot a node) on a profile as ordinary command
// definitions, so they appear in the device command picker and pass the same enqueue check,
// permissions and audit trail as every other command. It adds only what is missing, so
// pressing it twice (or on a profile that already declares one by hand) creates no duplicate
// and changes nothing that exists.
//
// Declaring a command does not make it reach a device: the device's Sparkplug source must
// have commands enabled, writes are limited to the metrics that source lists, and a reboot
// needs the source to allow it. The adapter refuses anything else with its reason.
//
// The keys below must equal the adapter's command keys.

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Plus } from 'lucide-react';
import { hasAuthority } from '@devicechain/client';
import { Button } from '@/components/ui/button';
import { useToast } from '@/components/ui/toast';
import { useAuth } from '@/auth/AuthProvider';
import { errMessage } from '@/routes/common';
import { createCommandDefinition, listCommandDefinitions } from '@/lib/api/device-management';

// Stored on the profile, a shared tenant resource: the canonical (English) text, not the
// language of whoever pressed the button. Editable afterwards like any command definition.
export const SPARKPLUG_STANDARD_COMMANDS = [
  {
    commandKey: 'sparkplug.write',
    slug: 'sparkplug-write',
    name: 'Write metric',
    // The payload is {"metric": name, "value": scalar}. It carries no parameter schema: the
    // metric and its datatype belong to the node's birth, and the console's form for this key
    // sends the two fields as typed.
    impact: 'NORMAL',
    description:
      "Writes a value to one metric of the node or device (Sparkplug NCMD / DCMD). The metric must be on its source's writable list, and the result is confirmed by the node reporting the value.",
  },
  {
    commandKey: 'sparkplug.rebirth',
    slug: 'sparkplug-rebirth',
    name: 'Rebirth',
    impact: 'NORMAL',
    description: 'Asks the node to publish its birth again, with every current value.',
  },
  {
    commandKey: 'sparkplug.reboot',
    slug: 'sparkplug-reboot',
    name: 'Reboot',
    // A reboot interrupts the node, so it waits for the device type's maintenance window; the
    // adapter also requires the DISRUPTIVE (or DESTRUCTIVE) impact before it will send one.
    impact: 'DISRUPTIVE',
    description:
      'Reboots the node (Node Control/Reboot). The source must allow reboots and the node must declare the metric.',
  },
] as const;

export function SparkplugStandardCommands({
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
      const missing = SPARKPLUG_STANDARD_COMMANDS.filter((c) => !existing.has(c.commandKey));
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
          ? t('sparkplugCommandsAlreadyDeclared')
          : t('sparkplugCommandsAdded', { count: missing.length }),
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
      <p className="max-w-prose text-sm text-muted-foreground">{t('sparkplugCommandsDescription')}</p>
      <Button size="sm" variant="outline" onClick={add} loading={busy} disabled={busy} className="shrink-0">
        <Plus size={16} /> {t('sparkplugCommandsAddButton')}
      </Button>
    </div>
  );
}
