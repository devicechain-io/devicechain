// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The tenant tier catalog (ADR-065), in the operator's arranged order (S5c). Rows are
// drag-reorderable; the order persists on drop via reorderTenantTiers, which is
// presentation only — it moves where a tier appears, never what it grants.
//
// Ordering is deliberately NOT a rank: dragging gold above bronze says nothing about one
// containing the other (ADR-065 rejected a tier ordinal for exactly that reason). It is a
// shelf, not a ladder.

import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Plus, GripVertical } from 'lucide-react';
import {
  DndContext,
  closestCenter,
  KeyboardSensor,
  PointerSensor,
  useSensor,
  useSensors,
  type DragEndEvent,
} from '@dnd-kit/core';
import {
  SortableContext,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
  arrayMove,
} from '@dnd-kit/sortable';
import { CSS } from '@dnd-kit/utilities';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { TierPill } from '@/components/tiers/TierPill';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import {
  DataTable,
  DataTableHead,
  DataTableHeaderCell,
  DataTableBody,
  DataTableRow,
  DataTableCell,
} from '@/components/ui/data-table';
import { useToast } from '@/components/ui/toast';
import { useQuery } from '@/lib/hooks/use-query';
import {
  listTenantTierCatalog,
  reorderTenantTiers,
  type AdminTenantTierDetail,
} from '@/lib/api/admin';
import { useReload, errMessage } from '@/routes/common';

export default function TiersPage() {
  const navigate = useNavigate();
  const { toast } = useToast();
  const [version, reload] = useReload();
  const { data, loading, error } = useQuery(listTenantTierCatalog, [version]);

  // Local working copy so a drag reorders instantly. Reset from the server on every
  // (re)load, so a rejected reorder — or another operator's change — snaps back to truth.
  const [order, setOrder] = useState<AdminTenantTierDetail[]>([]);
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    if (data) setOrder(data);
  }, [data]);

  const sensors = useSensors(
    useSensor(PointerSensor),
    // The keyboard sensor keeps drag-reordering accessible: focus a handle, space to
    // lift, arrows to move. Without it the feature is mouse-only.
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );

  const onDragEnd = async (e: DragEndEvent) => {
    // Guard against a second reorder landing while one is in flight — the keyboard path
    // can fire onDragEnd even though the pointer-events lock covers the mouse, and two
    // concurrent reorderTenantTiers calls would race.
    if (saving) return;
    const { active, over } = e;
    if (!over || active.id === over.id) return;
    const from = order.findIndex((t) => t.token === active.id);
    const to = order.findIndex((t) => t.token === over.id);
    if (from < 0 || to < 0) return;

    const prev = order;
    const next = arrayMove(order, from, to);
    setOrder(next); // optimistic
    setSaving(true);
    try {
      await reorderTenantTiers(next.map((t) => t.token));
      toast('Tier order saved');
      // No reload on success: `next` already IS the order the server committed (the
      // mutation validated and persisted exactly this token order), so refetching would
      // only open a window where a stale in-flight read could clobber a subsequent drag.
    } catch (err) {
      setOrder(prev); // roll the view back to before the drag
      toast(errMessage(err), 'error');
      reload(); // and reconcile with the authoritative order
    } finally {
      setSaving(false);
    }
  };

  return (
    <PageShell
      title="Tiers"
      description="The packaging a tenant is sold. Drag to arrange how tiers are listed — order is presentation only."
      action={
        <Button onClick={() => navigate('/admin/tiers/new')}>
          <Plus size={16} /> New tier
        </Button>
      }
    >
      <div className="space-y-6">
        {/* Once a list has rendered, keep showing it on a failed reload rather than
            blanking to an error — but surface the failure instead of hiding it. */}
        {error && order.length > 0 && (
          <p className="text-sm text-destructive">Could not refresh tiers ({error}).</p>
        )}
        {loading && order.length === 0 ? (
          <LoadingState description="Loading tiers…" />
        ) : error && order.length === 0 ? (
          <ErrorState description={error} />
        ) : order.length === 0 ? (
          <EmptyState description="No tiers defined yet." />
        ) : (
          <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={onDragEnd}>
            <SortableContext items={order.map((t) => t.token)} strategy={verticalListSortingStrategy}>
              <DataTable>
                <DataTableHead>
                  <DataTableHeaderCell className="w-8"> </DataTableHeaderCell>
                  <DataTableHeaderCell>Tier</DataTableHeaderCell>
                  <DataTableHeaderCell>Description</DataTableHeaderCell>
                  <DataTableHeaderCell className="text-right">Tenants</DataTableHeaderCell>
                </DataTableHead>
                <DataTableBody>
                  {order.map((t) => (
                    <TierRow
                      key={t.token}
                      tier={t}
                      saving={saving}
                      onOpen={() => navigate(`/admin/tiers/${encodeURIComponent(t.token)}`)}
                    />
                  ))}
                </DataTableBody>
              </DataTable>
            </SortableContext>
          </DndContext>
        )}
      </div>
    </PageShell>
  );
}

function TierRow({
  tier,
  saving,
  onOpen,
}: {
  tier: AdminTenantTierDetail;
  saving: boolean;
  onOpen: () => void;
}) {
  const {
    attributes,
    listeners,
    setNodeRef,
    setActivatorNodeRef,
    transform,
    transition,
    isDragging,
  } = useSortable({ id: tier.token, disabled: saving });

  const openOnKey = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter') {
      e.preventDefault();
      onOpen();
    }
  };

  return (
    <DataTableRow
      ref={setNodeRef}
      style={{
        transform: CSS.Transform.toString(transform),
        transition,
        ...(isDragging ? { position: 'relative', zIndex: 10 } : {}),
      }}
      role="button"
      tabIndex={0}
      onClick={onOpen}
      onKeyDown={openOnKey}
      className={`cursor-pointer ${isDragging ? 'bg-muted shadow' : ''}`}
    >
      <DataTableCell>
        {/* The grip is the only drag affordance; stopPropagation so activating it does
            not also open the tier. setActivatorNodeRef tells dnd-kit this is the handle,
            which keeps keyboard focus restoration correct after a drop. */}
        <button
          type="button"
          ref={setActivatorNodeRef}
          className="cursor-grab touch-none rounded p-1 text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring active:cursor-grabbing disabled:cursor-not-allowed disabled:opacity-40"
          aria-label={`Reorder ${tier.name ?? tier.token}`}
          disabled={saving}
          onClick={(e) => e.stopPropagation()}
          {...attributes}
          {...listeners}
        >
          <GripVertical size={16} />
        </button>
      </DataTableCell>
      <DataTableCell>
        <div className="flex items-center gap-3">
          <TierPill label={tier.token} color={tier.color} />
          <span className="font-medium">{tier.name ?? tier.token}</span>
        </div>
      </DataTableCell>
      <DataTableCell className="max-w-md truncate text-muted-foreground">
        {tier.description ?? '—'}
      </DataTableCell>
      <DataTableCell className="text-right">
        <Badge variant="secondary">
          {tier.tenantCount} tenant{tier.tenantCount === 1 ? '' : 's'}
        </Badge>
      </DataTableCell>
    </DataTableRow>
  );
}
