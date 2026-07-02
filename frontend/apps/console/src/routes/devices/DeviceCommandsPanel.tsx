// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { Send } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';
import { useToast } from '@/components/ui/toast';
import { formatTime } from '@/lib/utils';
import { useQuery } from '@/lib/hooks/use-query';
import { errMessage, useReload } from '@/routes/common';
import {
  listCommands,
  createCommand,
  cancelCommand,
  type Command,
} from '@/lib/api/command-delivery';

const pageSize = 25;

// Commands that have reached a terminal state can no longer be cancelled; the
// rest (QUEUED / SENT / DELIVERED) are still in flight.
const TERMINAL = new Set(['SUCCESSFUL', 'TIMEOUT', 'EXPIRED', 'FAILED']);

function statusVariant(status: string): 'success' | 'destructive' | 'outline' | 'secondary' {
  switch (status) {
    case 'SUCCESSFUL':
      return 'success';
    case 'FAILED':
    case 'TIMEOUT':
      return 'destructive';
    case 'EXPIRED':
      return 'outline';
    default:
      // QUEUED / SENT / DELIVERED — still in flight.
      return 'secondary';
  }
}

// DeviceCommandsPanel lets an operator issue a command to a device and shows the
// per-device command history with live lifecycle status — this is also the
// device's command-execution audit trail. It loads independently of the rest of
// the page: if the tenant's role lacks command:read the query errors and this
// panel shows an ErrorState rather than breaking the page.
export function DeviceCommandsPanel({ deviceToken }: { deviceToken: string }) {
  const { toast } = useToast();
  const [name, setName] = useState('');
  const [payload, setPayload] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [version, reload] = useReload();

  const { data, loading, error } = useQuery(
    () => listCommands({ deviceToken, pageNumber: 1, pageSize }),
    [deviceToken, version],
  );

  const issue = async () => {
    const trimmed = name.trim();
    if (!trimmed) {
      toast('Command name is required', 'error');
      return;
    }
    setSubmitting(true);
    try {
      await createCommand({
        token: crypto.randomUUID(),
        deviceToken,
        name: trimmed,
        payload: payload.trim() || undefined,
      });
      toast(`Command “${trimmed}” issued`);
      setName('');
      setPayload('');
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setSubmitting(false);
    }
  };

  const cancel = async (command: Command) => {
    try {
      await cancelCommand(command.token);
      toast(`Command “${command.name}” cancelled`);
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  const commands = data?.results ?? [];

  return (
    <div className="space-y-6">
      {/* Issue form. A command:write failure surfaces as a toast on submit. */}
      <div className="space-y-3">
        <div className="grid gap-3 sm:grid-cols-2">
          <FormField label="Command name" description="The command the device knows how to handle.">
            <Input
              value={name}
              placeholder="e.g. reboot"
              onChange={(e) => setName(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') issue();
              }}
            />
          </FormField>
          <FormField label="Payload" description="Optional. Sent to the device verbatim (e.g. JSON).">
            <textarea
              className="min-h-[2.25rem] w-full rounded-md border border-input bg-background px-3 py-1.5 text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-ring"
              rows={1}
              value={payload}
              placeholder='{"delaySeconds": 5}'
              onChange={(e) => setPayload(e.target.value)}
            />
          </FormField>
        </div>
        <Button onClick={issue} loading={submitting} disabled={submitting}>
          <Send size={14} /> Issue command
        </Button>
      </div>

      {/* History / audit trail. */}
      {loading ? (
        <LoadingState description="Loading commands…" />
      ) : error ? (
        <ErrorState description={error} />
      ) : commands.length === 0 ? (
        <EmptyState description="No commands have been issued to this device yet." />
      ) : (
        <DataTable>
          <DataTableHead>
            <DataTableHeaderCell>Queued</DataTableHeaderCell>
            <DataTableHeaderCell>Name</DataTableHeaderCell>
            <DataTableHeaderCell>Status</DataTableHeaderCell>
            <DataTableHeaderCell>Result</DataTableHeaderCell>
            <DataTableHeaderCell>&nbsp;</DataTableHeaderCell>
          </DataTableHead>
          <DataTableBody>
            {commands.map((command) => (
              <DataTableRow key={command.id}>
                <DataTableCell className="whitespace-nowrap text-muted-foreground">
                  {formatTime(command.queuedTime)}
                </DataTableCell>
                <DataTableCell className="font-medium text-foreground">{command.name}</DataTableCell>
                <DataTableCell>
                  <Badge variant={statusVariant(command.status)}>{command.status}</Badge>
                </DataTableCell>
                <DataTableCell className="max-w-xs truncate text-muted-foreground">
                  {command.error || command.responsePayload || '—'}
                </DataTableCell>
                <DataTableCell className="text-right">
                  {!TERMINAL.has(command.status) && (
                    <Button variant="outline" size="sm" onClick={() => cancel(command)}>
                      Cancel
                    </Button>
                  )}
                </DataTableCell>
              </DataTableRow>
            ))}
          </DataTableBody>
        </DataTable>
      )}
    </div>
  );
}
