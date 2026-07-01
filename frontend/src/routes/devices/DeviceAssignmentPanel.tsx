// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { Plus, Trash2 } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Combobox } from '@/components/ui/combobox';
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
import { useQuery } from '@/lib/hooks/use-query';
import { errMessage, useReload } from '@/routes/common';
import { listCustomers } from '@/lib/api/customers';
import { listAreas } from '@/lib/api/areas';
import { listAssets } from '@/lib/api/assets';
import {
  listDeviceAssignments,
  assignDevice,
  unassignDevice,
  ASSIGNMENT_TARGET_TYPES,
  type AssignmentTargetType,
  type EntityRelationship,
} from '@/lib/api/relationships';

// Load the selectable targets for an assignment target type. Each option's value
// is the entity token (what the edge stores); the label prefers its name.
async function loadTargets(type: AssignmentTargetType): Promise<{ value: string; label: string }[]> {
  const opts = { pageNumber: 1, pageSize: 200 };
  const results =
    type === 'customer'
      ? (await listCustomers(opts)).results
      : type === 'area'
        ? (await listAreas(opts)).results
        : (await listAssets(opts)).results;
  return results.map((r) => ({ value: r.token, label: r.name || r.token }));
}

// DeviceAssignmentPanel manages a device's assignments — tracked relationships to
// a customer/area/asset (ADR-013). A device may hold several; each becomes an
// anchor on the device's events, so its telemetry is queryable by every dimension
// (ADR-013 addendum). An unassigned device still emits telemetry — resolution is
// anchorless, not dropped. Loads independently: without device:read the query
// errors and this panel degrades to an ErrorState.
export function DeviceAssignmentPanel({ deviceToken }: { deviceToken: string }) {
  const { toast } = useToast();
  const [targetType, setTargetType] = useState<AssignmentTargetType>('customer');
  const [target, setTarget] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [version, reload] = useReload();

  const { data, loading, error } = useQuery(
    () => listDeviceAssignments(deviceToken),
    [deviceToken, version],
  );

  // The selectable targets for the chosen type; reloads when the type changes.
  const { data: targets, loading: targetsLoading } = useQuery(() => loadTargets(targetType), [targetType]);

  // Switching target type clears any half-made selection for the previous type.
  const changeType = (t: AssignmentTargetType) => {
    setTargetType(t);
    setTarget('');
  };

  const assign = async () => {
    if (!target) {
      toast('Choose a target to assign', 'error');
      return;
    }
    setSubmitting(true);
    try {
      await assignDevice(deviceToken, targetType, target);
      toast('Device assigned');
      setTarget('');
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setSubmitting(false);
    }
  };

  const remove = async (edge: EntityRelationship) => {
    if (!window.confirm(`Unassign this device from ${edge.targetType} “${edge.target?.token}”?`)) {
      return;
    }
    try {
      await unassignDevice(edge.token);
      toast('Assignment removed');
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  const assignments = data ?? [];

  return (
    <div className="space-y-6">
      {/* Assign form, grouped in a contrasting container. */}
      <div className="flex flex-wrap items-end gap-3 rounded-lg border border-border bg-muted/40 p-4">
        <div className="w-40">
          <FormField label="Target type">
            <Combobox
              options={ASSIGNMENT_TARGET_TYPES.map((t) => ({ value: t, label: t[0].toUpperCase() + t.slice(1) }))}
              value={targetType}
              onChange={(v) => changeType(v as AssignmentTargetType)}
              allowClear={false}
            />
          </FormField>
        </div>
        <div className="w-64">
          <FormField label="Target">
            <Combobox
              options={targets ?? []}
              value={target}
              onChange={setTarget}
              placeholder={targetsLoading ? 'Loading…' : `Select a ${targetType}…`}
              emptyMessage={`No ${targetType}s found.`}
            />
          </FormField>
        </div>
        <Button onClick={assign} loading={submitting} disabled={submitting}>
          <Plus size={14} /> Assign
        </Button>
      </div>

      {/* Current assignments. */}
      {loading ? (
        <LoadingState description="Loading assignments…" />
      ) : error ? (
        <ErrorState description={error} />
      ) : assignments.length === 0 ? (
        <EmptyState description="This device is not assigned. Its telemetry still flows — assign it to give its events a customer/area/asset anchor." />
      ) : (
        <DataTable>
          <DataTableHead>
            <DataTableHeaderCell>Type</DataTableHeaderCell>
            <DataTableHeaderCell>Target</DataTableHeaderCell>
            <DataTableHeaderCell>&nbsp;</DataTableHeaderCell>
          </DataTableHead>
          <DataTableBody>
            {assignments.map((edge) => (
              <DataTableRow key={edge.id}>
                <DataTableCell>
                  <Badge variant="secondary">{edge.targetType}</Badge>
                </DataTableCell>
                <DataTableCell className="font-mono text-xs text-foreground">
                  {edge.target?.token ?? '—'}
                </DataTableCell>
                <DataTableCell className="text-right">
                  <Button variant="outline" size="sm" onClick={() => remove(edge)}>
                    <Trash2 size={13} /> Unassign
                  </Button>
                </DataTableCell>
              </DataTableRow>
            ))}
          </DataTableBody>
        </DataTable>
      )}
    </div>
  );
}
