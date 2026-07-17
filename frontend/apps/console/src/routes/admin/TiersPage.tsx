// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The tenant tier catalog (ADR-065), in the operator's arranged order (S5c). Rows are
// drag-reorderable; the order persists on drop via reorderTenantTiers, which is
// presentation only — it moves where a tier appears, never what it grants. Each row shows
// the tier's colored pill so the catalog reads at a glance the way a tenant list will.
//
// Ordering is deliberately NOT a rank: dragging gold above bronze says nothing about one
// containing the other (ADR-065 rejected a tier ordinal for exactly that reason). It is a
// shelf, not a ladder.

import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Plus, GripVertical, ChevronRight } from 'lucide-react';
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

  // Local working copy so a drag reorders instantly, before the server round-trip. It is
  // reset from the server on every (re)load, so a rejected reorder — or another operator's
  // change — snaps back to the truth.
  const [order, setOrder] = useState<AdminTenantTierDetail[]>([]);
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    if (data) setOrder(data);
  }, [data]);

  const sensors = useSensors(
    useSensor(PointerSensor),
    // The keyboard sensor is what keeps drag-reordering accessible: focus a handle, space
    // to lift, arrows to move, space to drop. Without it the feature is mouse-only.
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );

  const onDragEnd = async (e: DragEndEvent) => {
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
    } catch (err) {
      setOrder(prev); // roll the view back to what it was before the drag
      toast(errMessage(err), 'error');
    } finally {
      setSaving(false);
      // Reconcile with the server regardless: on success this is a no-op, on a rejected
      // reorder it replaces the optimistic guess with the authoritative order.
      reload();
    }
  };

  return (
    <PageShell
      title="Tiers"
      description="The packaging a tenant is sold. Drag to arrange how tiers are listed across the console — order is presentation only and implies no ranking; a tier does not contain the ones below it. Each tier sets the defaults its tenants are held to, and editing one moves every tenant at it within a minute, no restart."
      action={
        <Button onClick={() => navigate('/admin/tiers/new')}>
          <Plus size={16} /> New tier
        </Button>
      }
    >
      <div className="space-y-6">
        {loading && order.length === 0 ? (
          <LoadingState description="Loading tiers…" />
        ) : error && order.length === 0 ? (
          <ErrorState description={error} />
        ) : order.length === 0 ? (
          <EmptyState description="No tiers defined yet." />
        ) : (
          <div className="overflow-hidden rounded-lg border border-border bg-card">
            <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={onDragEnd}>
              <SortableContext
                items={order.map((t) => t.token)}
                strategy={verticalListSortingStrategy}
              >
                <ul className={saving ? 'pointer-events-none opacity-70' : undefined}>
                  {order.map((t) => (
                    <TierRow
                      key={t.token}
                      tier={t}
                      onOpen={() => navigate(`/admin/tiers/${encodeURIComponent(t.token)}`)}
                    />
                  ))}
                </ul>
              </SortableContext>
            </DndContext>
          </div>
        )}
      </div>
    </PageShell>
  );
}

function TierRow({ tier, onOpen }: { tier: AdminTenantTierDetail; onOpen: () => void }) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({
    id: tier.token,
  });

  return (
    <li
      ref={setNodeRef}
      style={{ transform: CSS.Transform.toString(transform), transition }}
      className={`flex items-center gap-3 border-b border-border px-3 py-3 last:border-b-0 ${
        isDragging ? 'relative z-10 bg-muted shadow' : 'bg-card'
      }`}
    >
      {/* Only the handle initiates a drag, so the rest of the row stays clickable to open
          the tier. The handle carries the dnd listeners + attributes, which include the
          keyboard interactions and the aria wiring. */}
      <button
        type="button"
        ref={undefined}
        className="cursor-grab touch-none rounded p-1 text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring active:cursor-grabbing"
        aria-label={`Reorder ${tier.name ?? tier.token}`}
        {...attributes}
        {...listeners}
      >
        <GripVertical size={16} />
      </button>

      {/* The rest of the row opens the tier. Hover + a chevron make it read as a link;
          without them the detail page was reachable but invisible. */}
      <button
        type="button"
        onClick={onOpen}
        className="group -mx-2 flex flex-1 items-center gap-3 rounded px-2 py-1 text-left hover:bg-muted/60 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
      >
        {/* The pill carries the token (lowercase, fixed width, colored). */}
        <TierPill label={tier.token} color={tier.color} />
        {/* The human name is the primary label, to the right of the pill, capitalized as
            entered. Falls back to the token only when a tier has no name. */}
        <span className="font-medium">{tier.name ?? tier.token}</span>
        <span className="max-w-md flex-1 truncate text-sm text-muted-foreground">
          {tier.description ?? 'No description'}
        </span>
        <Badge variant="secondary">
          {tier.tenantCount} tenant{tier.tenantCount === 1 ? '' : 's'}
        </Badge>
        <ChevronRight
          size={16}
          className="text-muted-foreground/50 transition group-hover:text-muted-foreground"
        />
      </button>
    </li>
  );
}
