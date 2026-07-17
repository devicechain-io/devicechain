// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The per-tier AI-packaging panel and the mutation logic behind it, shared by the two
// screens that show it: the cross-tier matrix (AiPackagingPage, every tier at once) and a
// single tier's detail tab (TierAiModelsPanel). Both render the SAME control — one row per
// provider, two columns (grant, default) — and issue the SAME grant/default mutations, so
// the invariants and their confirmations live here once rather than being re-derived per
// screen.
//
// WHY GRANT AND DEFAULT ARE SEPARATE, AND STAY SEPARATE. The server never infers a
// default: a tier that grants models and marks none resolves to NO model, even when it
// grants exactly one (model/grant.go carries the history of that shape shipping as a bug
// five times). Pre-selecting a default in the UI and having the operator confirm it is a
// choice; the same pre-selection made server-side is an inference. This panel shows the
// grant and the default as the two separate facts they are, and says plainly when a tier
// will resolve to none — it is that behaviour's only mitigation.

import { useState } from 'react';
import { Link } from 'react-router-dom';
import { AlertTriangle } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { Checkbox } from '@/components/ui/checkbox';
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group';
import { SectionPanel } from '@/components/ui/section-panel';
import {
  DataTable,
  DataTableHead,
  DataTableHeaderCell,
  DataTableBody,
  DataTableRow,
  DataTableCell,
} from '@/components/ui/data-table';
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import {
  listAiProviderTierGrants,
  grantAiProviderToTier,
  revokeAiProviderFromTier,
  setAiTierDefault,
  clearAiTierDefault,
  type AiProviderListItem,
} from '@/lib/api/ai-inference-admin';
import { errMessage } from '@/routes/common';
import { tierWarning, warningText, type PackagingTier } from './aiPackaging';

// Providers are instance config an operator hand-registers, so the realistic count is a
// handful and one page holds them all. Every provider needs a row (an ungranted one still
// has to be grantable), and the list API is the paginated one — so ask for more than
// anyone will have, and say so plainly if that ever stops being true rather than silently
// rendering a partial matrix.
export const PROVIDER_PAGE_SIZE = 200;

// The radio value standing for "this tier marks no default". A provider token can never
// collide with it: core.ValidateToken's grammar is ^[A-Za-z0-9][A-Za-z0-9_-]*$, so a real
// token cannot begin with an underscore. The sentinel is safe by the grammar, not by
// convention.
export const NO_DEFAULT = '__no_default__';

// useTierPackaging owns the grant/default mutations for one or many tiers: the single
// in-flight guard, the strand-a-tenant confirmations, and the reload-on-settle so every
// control repaints from server truth. `reload` is the caller's refetch of the queries this
// panel is rendered from — the hook does not own the data, only the writes against it.
export function useTierPackaging(reload: () => void) {
  const { toast } = useToast();
  const confirm = useConfirm();
  // One in-flight mutation at a time. The controls are cheap and each panel is small, so
  // freezing them all is simpler than per-cell pending state — and it removes the race
  // where two fast clicks on the same tier's default interleave.
  const [busy, setBusy] = useState(false);

  const run = async (action: () => Promise<unknown>, ok: string) => {
    setBusy(true);
    try {
      await action();
      toast(ok);
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
      // Reload on failure too: every control is rendered from server data, so refetching
      // is what snaps an optimistically-flipped checkbox back to the truth rather than
      // leaving the operator looking at a state the server rejected.
      reload();
    } finally {
      setBusy(false);
    }
  };

  // WHAT GETS CONFIRMED, AND WHY IT IS NOT EVERY CHANGE. The acts that STRAND tenants are
  // confirmed: revoking the tier's default, and clearing it. Both leave the tier resolving
  // to no model for every tenant that never chose one, and the server promotes nothing in
  // their place. Re-POINTING the default from one granted model to another is not
  // confirmed — it hands those tenants a different working model rather than none, it is
  // audited, and this screen shows the result immediately. Blast radius, not mutation
  // count, is what earns a dialog.
  const confirmStranding = (tier: PackagingTier, what: string, confirmLabel: string) =>
    confirm({
      title: `Leave ${tier.token} with no default model`,
      description: `${what} leaves ${tier.token} with no default — no other model is promoted in its place, so every tenant at ${tier.token} that has not chosen a model itself will resolve to no model.`,
      confirmLabel,
    });

  const toggleGrant = async (tier: PackagingTier, provider: string, granted: boolean) => {
    if (!granted) {
      await run(
        () => grantAiProviderToTier(tier.token, provider),
        `“${provider}” is on the ${tier.token} menu`,
      );
      return;
    }

    // An unknown tier has no tenants and can have none, so revoking its grant strands
    // nobody — the same reason tierWarning stays quiet about it. Confirming here would
    // claim a consequence that cannot happen.
    if (tier.known) {
      // Re-read before deciding. This guard was answering from the last fetch, which goes
      // stale the moment another operator marks a default in another session: their mark
      // is deleted with no warning by the one click the dialog exists for. The server is
      // authoritative and this is advisory, so a failed re-read falls back to the fetched
      // view rather than blocking the act.
      let isDefault = tier.defaultProvider === provider;
      try {
        const fresh = await listAiProviderTierGrants();
        isDefault = fresh.some(
          (g) => g.tier === tier.token && g.provider.token === provider && g.isDefault,
        );
      } catch {
        // Keep the fetched answer.
      }
      if (
        isDefault &&
        !(await confirmStranding(tier, `“${provider}” is ${tier.token}'s default. Revoking it`, 'Revoke'))
      ) {
        return;
      }
    }

    await run(
      () => revokeAiProviderFromTier(tier.token, provider),
      `“${provider}” is off the ${tier.token} menu`,
    );
  };

  const chooseDefault = async (tier: PackagingTier, value: string) => {
    if (value === NO_DEFAULT) {
      // Radix moves selection with the arrow keys, so this can be reached without a click
      // — one keypress away from stranding every non-choosing tenant on the tier. The
      // dialog is what makes it a decision. Skipped when the tier has nothing to strand
      // with: no grants, or no tenants that could ever be at it.
      if (
        tier.known &&
        tier.granted.size > 0 &&
        !(await confirmStranding(tier, 'Clearing the default', 'Clear'))
      ) {
        return;
      }
      await run(() => clearAiTierDefault(tier.token), `${tier.token} has no default model`);
      return;
    }
    await run(() => setAiTierDefault(tier.token, value), `${tier.token} defaults to “${value}”`);
  };

  return { busy, toggleGrant, chooseDefault };
}

// TierPanel renders one tier's grant/default matrix. Presentation only — every decision
// (what to confirm, what to write) lives in useTierPackaging above; this maps a
// PackagingTier and the provider list to controls and hands clicks back to the handlers.
//
// showHeader draws the tier's token/name/tenant-count strip above the table. The matrix
// screen stacks many panels and needs each one labelled, so it defaults on; a single
// tier's detail tab already shows all three in the page header, so it turns this off to
// avoid the duplicate.
export function TierPanel({
  tier,
  providers,
  busy,
  onToggleGrant,
  onChooseDefault,
  showHeader = true,
}: {
  tier: PackagingTier;
  providers: AiProviderListItem[];
  busy: boolean;
  onToggleGrant: (tier: PackagingTier, provider: string, granted: boolean) => void;
  onChooseDefault: (tier: PackagingTier, value: string) => void;
  showHeader?: boolean;
}) {
  const warning = tierWarning(tier);
  // The marked default is normally one of the rows below. It is not when the provider list
  // was truncated past it — and then the group's value names an item that does not exist,
  // so NOTHING renders as selected: a state the database forbids (a tier has a default or
  // it does not), and one that invites the operator to "fix" it by choosing No default,
  // silently clearing a real default they were never shown. Give the mark a row of its own
  // instead, so the group always has the item its value names.
  const markedIsListed =
    tier.defaultProvider === null || providers.some((p) => p.token === tier.defaultProvider);

  return (
    <SectionPanel
      title={showHeader ? tier.token : undefined}
      description={showHeader ? (tier.name ?? undefined) : undefined}
      action={
        showHeader ? (
          tier.known ? (
            <Badge variant="secondary">
              {tier.tenantCount} tenant{tier.tenantCount === 1 ? '' : 's'}
            </Badge>
          ) : (
            <Badge variant="outline">unknown tier</Badge>
          )
        ) : undefined
      }
    >
      <div className="space-y-4">
        {!tier.known && (
          <p className="text-sm text-muted-foreground">
            No tier with this token exists, so nothing resolves through these grants — no tenant
            can report a tier the catalog does not have. It is shown because this is the only
            screen that can reveal it: ai-inference cannot check a tier token when a grant is
            written, since the catalog lives on a plane its credential cannot reach. Untick every
            model below to clear it.
          </p>
        )}

        {warning && (
          <div className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 p-3 text-sm">
            <AlertTriangle size={16} className="mt-0.5 shrink-0 text-amber-500" aria-hidden />
            <span>{warningText(warning, tier)}</span>
          </div>
        )}

        {/* One RadioGroup per tier: Radix gives it "exactly one selected", arrow-key
            movement and role="radiogroup" for free, which is the same invariant
            uix_ai_tier_grant_default enforces in the database. Items need only be
            descendants, so the group can wrap the table and its items sit in cells. */}
        <RadioGroup
          value={tier.defaultProvider ?? NO_DEFAULT}
          onValueChange={(v) => onChooseDefault(tier, v)}
          disabled={busy}
          className="gap-0"
          aria-label={`Default model for ${tier.token}`}
        >
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>Provider</DataTableHeaderCell>
              <DataTableHeaderCell className="w-24 text-center">Grant</DataTableHeaderCell>
              <DataTableHeaderCell className="w-24 text-center">Default</DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {providers.map((p) => {
                const granted = tier.granted.has(p.token);
                return (
                  <DataTableRow key={p.token}>
                    <DataTableCell>
                      <div className="flex items-center gap-2">
                        <Link
                          to={`/admin/ai-providers/${encodeURIComponent(p.token)}`}
                          className="font-medium hover:underline"
                        >
                          {p.token}
                        </Link>
                        {!p.enabled && <Badge variant="outline">disabled</Badge>}
                      </div>
                      <span className="text-xs text-muted-foreground">{p.model}</span>
                    </DataTableCell>
                    <DataTableCell className="text-center">
                      <div className="flex justify-center">
                        <Checkbox
                          checked={granted}
                          disabled={busy}
                          aria-label={`Grant ${p.token} to ${tier.token}`}
                          onCheckedChange={() => onToggleGrant(tier, p.token, granted)}
                        />
                      </div>
                    </DataTableCell>
                    <DataTableCell className="text-center">
                      <div className="flex justify-center">
                        {/* An ungranted provider cannot be a default — the server refuses
                            it rather than granting as a side effect — so the control is
                            disabled rather than hidden, which shows the operator the order
                            of operations instead of concealing it. */}
                        <RadioGroupItem
                          value={p.token}
                          disabled={busy || !granted}
                          aria-label={`Make ${p.token} the default for ${tier.token}`}
                          title={granted ? undefined : 'Grant this model first'}
                        />
                      </div>
                    </DataTableCell>
                  </DataTableRow>
                );
              })}

              {/* The marked default, when the truncated list has no row for it. Rendered
                  so the group's value always names a real item — see markedIsListed. */}
              {!markedIsListed && tier.defaultProvider !== null && (
                <DataTableRow>
                  <DataTableCell>
                    <div className="flex items-center gap-2">
                      <Link
                        to={`/admin/ai-providers/${encodeURIComponent(tier.defaultProvider)}`}
                        className="font-medium hover:underline"
                      >
                        {tier.defaultProvider}
                      </Link>
                      {!tier.defaultProviderEnabled && <Badge variant="outline">disabled</Badge>}
                    </div>
                    <span className="text-xs text-muted-foreground">
                      This tier’s default, below the provider-list page cut
                    </span>
                  </DataTableCell>
                  <DataTableCell className="text-center">
                    <div className="flex justify-center">
                      <Checkbox
                        checked
                        disabled={busy}
                        aria-label={`Grant ${tier.defaultProvider} to ${tier.token}`}
                        onCheckedChange={() =>
                          onToggleGrant(tier, tier.defaultProvider as string, true)
                        }
                      />
                    </div>
                  </DataTableCell>
                  <DataTableCell className="text-center">
                    <div className="flex justify-center">
                      <RadioGroupItem
                        value={tier.defaultProvider}
                        disabled={busy}
                        aria-label={`Make ${tier.defaultProvider} the default for ${tier.token}`}
                      />
                    </div>
                  </DataTableCell>
                </DataTableRow>
              )}

              {/* The explicit "no default" option. Without a row to select, clearing a
                  default is an act with no control, and "this tier deliberately has no
                  default" becomes a state an operator can only fall into rather than
                  choose. It is the same group, so choosing it visibly deselects whichever
                  model was marked. */}
              <DataTableRow className="bg-muted/30">
                <DataTableCell>
                  <span className="font-medium">No default</span>
                  <span className="block text-xs text-muted-foreground">
                    Tenants at this tier must each choose a model
                  </span>
                </DataTableCell>
                <DataTableCell className="text-center text-muted-foreground">—</DataTableCell>
                <DataTableCell className="text-center">
                  <div className="flex justify-center">
                    <RadioGroupItem
                      value={NO_DEFAULT}
                      disabled={busy}
                      aria-label={`${tier.token} has no default model`}
                    />
                  </div>
                </DataTableCell>
              </DataTableRow>
            </DataTableBody>
          </DataTable>
        </RadioGroup>
      </div>
    </SectionPanel>
  );
}
