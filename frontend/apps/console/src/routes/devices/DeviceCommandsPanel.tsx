// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useMemo, useState } from 'react';
import { Send } from 'lucide-react';
import type { CommandParameter } from '@devicechain/dashboards';
import {
  parseParameterSchema,
  defaultValues,
  validateParams,
  buildPayload,
  isScalar,
} from '@devicechain/widgets';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Input } from '@/components/ui/input';
import { Combobox } from '@/components/ui/combobox';
import { HintText } from '@/components/ui/hint-text';
import { FormField } from '@/components/ui/form-field';
import { CommandParameterForm } from './CommandParameterForm';
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
import { getDeviceCommandVocabulary, type PublishedCommand } from '@/lib/api/device-management';

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
  const [selectedKey, setSelectedKey] = useState('');
  const [paramValues, setParamValues] = useState<Record<string, string>>({});
  const [paramErrors, setParamErrors] = useState<Record<string, string>>({});
  const [submitting, setSubmitting] = useState(false);
  const [version, reload] = useReload();

  const { data, loading, error } = useQuery(
    () => listCommands({ deviceToken, pageNumber: 1, pageSize }),
    [deviceToken, version],
  );

  // The device's PUBLISHED vocabulary — what the enqueue gate will actually accept.
  // Loaded separately from the history so a vocabulary failure degrades the form
  // rather than blanking the panel.
  const { data: vocabulary, loading: vocabularyLoading } = useQuery(
    () => getDeviceCommandVocabulary(deviceToken),
    [deviceToken],
  );

  // Constrained means the profile declares a vocabulary and the gate rejects anything
  // outside it, so the form offers a picker. Unconstrained means the gate accepts any
  // key (ADR-043 decision 4) and free text is correct. While the vocabulary is loading —
  // or if reading it failed — fall back to free text: the server enforces either way, and
  // a form that refuses to render is worse than one that lets the server answer.
  const constrained = vocabulary?.constrained === true;
  const publishedCommands: PublishedCommand[] = vocabulary?.commands ?? [];
  const selected = publishedCommands.find((c) => c.commandKey === selectedKey);

  const params: CommandParameter[] = useMemo(
    () => parseParameterSchema(selected?.parameterSchema),
    [selected],
  );
  // A structured parameter can't be typed in the generated form. Rather than dead-end
  // the operator, such a command falls back to a raw payload box.
  const needsRawPayload = params.some((p) => !isScalar(p));

  // Seed the value map whenever the selected command changes, so declared defaults are
  // pre-filled and a previous command's values never leak into the next one's payload.
  useEffect(() => {
    setParamValues(defaultValues(params));
    setParamErrors({});
    setPayload('');
  }, [params]);

  const issue = async () => {
    // Unconstrained devices keep the free-text path; constrained ones send the key the
    // operator picked from the published vocabulary.
    const trimmed = constrained ? selectedKey : name.trim();
    if (!trimmed) {
      toast(constrained ? 'Select a command' : 'Command name is required', 'error');
      return;
    }

    let body: string | undefined;
    if (constrained && !needsRawPayload) {
      const errors = validateParams(params, paramValues);
      if (Object.keys(errors).length > 0) {
        setParamErrors(errors);
        toast('Fix the highlighted parameters', 'error');
        return;
      }
      setParamErrors({});
      body = buildPayload(params, paramValues);
    } else {
      body = payload.trim() || undefined;
    }

    setSubmitting(true);
    try {
      await createCommand({
        token: crypto.randomUUID(),
        deviceToken,
        name: trimmed,
        payload: body,
      });
      toast(`Command “${trimmed}” issued`);
      setName('');
      setPayload('');
      setSelectedKey('');
      setParamValues({});
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
        {constrained ? (
          <>
            <FormField
              label="Command"
              description="The commands this device's published profile declares."
            >
              <Combobox
                options={publishedCommands.map((c) => ({
                  value: c.commandKey,
                  label: c.name ? `${c.name} (${c.commandKey})` : c.commandKey,
                  description: c.description ?? undefined,
                }))}
                value={selectedKey}
                onChange={setSelectedKey}
                placeholder="Select a command"
              />
            </FormField>
            {selected && needsRawPayload ? (
              <FormField
                label="Payload"
                description="This command declares a structured parameter, so its payload is entered as JSON."
              >
                <textarea
                  className="min-h-[4rem] w-full rounded-md border border-input bg-background px-3 py-1.5 font-mono text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-ring"
                  rows={3}
                  value={payload}
                  placeholder='{"schedule": {"hour": 2}}'
                  onChange={(e) => setPayload(e.target.value)}
                />
              </FormField>
            ) : (
              <CommandParameterForm
                params={params}
                values={paramValues}
                errors={paramErrors}
                disabled={submitting}
                onChange={(paramName, value) =>
                  setParamValues((prev) => ({ ...prev, [paramName]: value }))
                }
              />
            )}
          </>
        ) : (
          <>
            <div className="grid gap-3 sm:grid-cols-2">
              <FormField
                label="Command name"
                description="The command the device knows how to handle."
              >
                <Input
                  value={name}
                  placeholder="e.g. reboot"
                  onChange={(e) => setName(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') issue();
                  }}
                />
              </FormField>
              <FormField
                label="Payload"
                description="Optional. Sent to the device verbatim (e.g. JSON)."
              >
                <textarea
                  className="min-h-[2.25rem] w-full rounded-md border border-input bg-background px-3 py-1.5 text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-ring"
                  rows={1}
                  value={payload}
                  placeholder='{"delaySeconds": 5}'
                  onChange={(e) => setPayload(e.target.value)}
                />
              </FormField>
            </div>
            {!vocabularyLoading && (
              <HintText>
                This device's profile declares no commands, so any command name is
                accepted. Declare commands on its device profile — and publish it — to get
                a typed form here.
              </HintText>
            )}
          </>
        )}
        <Button
          onClick={issue}
          loading={submitting}
          disabled={submitting || (constrained && !selectedKey)}
        >
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
