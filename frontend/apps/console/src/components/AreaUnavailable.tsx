// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The empty state a surface renders when its functional area was never deployed.
//
// It replaces the surface's BODY rather than removing the surface. Hiding a tab
// or nav entry outright would look tidier, but it makes the feature
// undiscoverable and leaves an operator unable to tell "this instance does not
// run that service" from "this build removed it" — and the first is a decision
// someone can revisit, which is the whole point of saying it out loud.
//
// It deliberately does NOT print a dcctl invocation or a Helm flag. The person
// looking at this screen is frequently not the person who can run either, and an
// instruction they cannot act on reads as a taunt. Naming the missing service is
// enough for them to ask the right question of the right person.

import { useTranslation } from 'react-i18next';
import { PackageX } from 'lucide-react';
import type { ReactNode } from 'react';
import { areaKey, type Area } from '@devicechain/client';
import { cn } from '@/lib/utils';
import { useAreaAbsent } from '@/lib/capabilities';

export function AreaUnavailable({ area, className }: { area: Area; className?: string }) {
  const { t } = useTranslation('common');
  return (
    <div
      className={cn('flex flex-col items-center justify-center gap-3 px-6 py-16 text-center', className)}
      data-testid="area-unavailable"
    >
      <PackageX className="size-8 text-muted-foreground" aria-hidden />
      <p className="text-base font-medium text-foreground">{t('areaUnavailableTitle')}</p>
      <p className="max-w-md text-sm text-muted-foreground">
        {t('areaUnavailableBody', { area: areaKey(area) })}
      </p>
    </div>
  );
}

/**
 * Renders `children`, or the not-deployed explanation when the area is known to
 * be absent.
 *
 * Wrapping the ROUTE ELEMENT rather than editing each page keeps the gate in one
 * readable place, and once the area set is known the gated component is never
 * mounted at all — so its fetch-on-mount never fires the request that would 405.
 * A gate inside the page would have to sit above every hook to manage even that,
 * and "above every hook" is a rule that survives exactly until the next edit.
 *
 * 🔴 Be precise about WHEN, because the obvious stronger claim is false: during
 * the first load the area set is still unknown, and 'unknown' renders the child
 * (the fail-open rule — see lib/capabilities.tsx). So on a cold page load a gated
 * child CAN mount once and fire one request before the answer arrives and
 * replaces it. Every navigation after that is clean, because the answer is
 * cached. Holding the child back until the answer settled would fix that one
 * request at the cost of blanking the surface whenever discovery hangs — trading
 * a wasted request for a hidden feature, which is the wrong way round.
 */
export function AreaGate({ area, children }: { area: Area; children: ReactNode }) {
  const absent = useAreaAbsent(area);
  if (absent) return <AreaUnavailable area={area} />;
  return <>{children}</>;
}

/**
 * A compact one-line variant for inline controls — a picker inside a form, where
 * the full centred empty state would dwarf the field it replaces.
 *
 * It carries the same sentence as the block variant so the two cannot drift into
 * telling different stories about the same instance.
 */
export function AreaUnavailableInline({ area }: { area: Area }) {
  const { t } = useTranslation('common');
  return (
    <p
      className="rounded-md border border-dashed px-3 py-2 text-xs text-muted-foreground"
      data-testid="area-unavailable-inline"
    >
      {t('areaUnavailableBody', { area: areaKey(area) })}
    </p>
  );
}
