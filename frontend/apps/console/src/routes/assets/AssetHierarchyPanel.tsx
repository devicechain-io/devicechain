// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { ChevronRight, CornerLeftUp, Unlink } from 'lucide-react';
import { Button } from '@/components/ui/button';
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
import {
  listAssets,
  listAssetAncestors,
  listAssetChildren,
  setAssetParent,
  clearAssetParent,
} from '@/lib/api/assets';

// How many candidate parents the picker offers. The same bound every other entity
// picker in the console uses; a tenant past it authors the hierarchy from the other
// end (open the intended parent and place this asset from its children list).
const CANDIDATE_PAGE = { pageNumber: 1, pageSize: 200 };

// AssetHierarchyPanel is the asset side of the parent/child tree: where this asset
// sits (a breadcrumb up to the root), what sits directly under it, and the controls
// to move it.
//
// The hierarchy is not a field on the asset — it is an edge of the reserved
// "contains" relationship type — so everything here goes through the hierarchy API
// rather than through the asset form. Nothing in this panel pre-checks whether a
// move is legal: the server refuses a self-parent, a cycle and an over-deep tree,
// and its message is what the operator sees. A console that duplicated those rules
// would be a second, drifting copy of them.
export function AssetHierarchyPanel({ assetToken }: { assetToken: string }) {
  const { t } = useTranslation('entities');
  const { toast } = useToast();
  const [parentToken, setParentToken] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [version, reload] = useReload();

  // Ancestors come back NEAREST FIRST; the breadcrumb reads root-first, so a COPY
  // is reversed here. Reversing in place would mutate the query's cached array and
  // flip the order again on every re-render.
  const ancestorsQuery = useQuery(() => listAssetAncestors(assetToken), [assetToken, version]);
  const childrenQuery = useQuery(
    () => listAssetChildren(assetToken, { pageNumber: 1, pageSize: 100 }),
    [assetToken, version],
  );
  // Every asset is a candidate parent except this one. The server refuses a cycle,
  // so the list is not pruned to non-descendants — pruning it here would mean
  // re-deriving the tree in the browser to hide options the server already guards.
  const candidatesQuery = useQuery(() => listAssets(CANDIDATE_PAGE), [version]);

  const place = async () => {
    if (!parentToken) {
      toast(t('assetParentRequired'), 'error');
      return;
    }
    setSubmitting(true);
    try {
      await setAssetParent(assetToken, parentToken);
      toast(t('assetParentSet'));
      setParentToken('');
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setSubmitting(false);
    }
  };

  const detach = async () => {
    setSubmitting(true);
    try {
      const removed = await clearAssetParent(assetToken);
      // The API distinguishes "detached one" from "already a root", so the toast
      // does too rather than reporting a change that did not happen.
      toast(removed ? t('assetParentCleared') : t('assetAlreadyRoot'));
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setSubmitting(false);
    }
  };

  const ancestors = ancestorsQuery.data ?? [];
  const breadcrumb = [...ancestors].reverse();
  const children = childrenQuery.data?.results ?? [];
  const candidates = (candidatesQuery.data?.results ?? []).filter((a) => a.token !== assetToken);

  return (
    <div className="space-y-6">
      {/* Where this asset sits. An asset with no ancestors is a root, which is the
          ordinary state and is said so rather than left blank. */}
      <div className="space-y-2">
        <div className="text-sm font-medium text-foreground">{t('assetPathTitle')}</div>
        {ancestorsQuery.loading ? (
          <LoadingState description={t('assetPathLoading')} />
        ) : ancestorsQuery.error ? (
          <ErrorState description={ancestorsQuery.error} />
        ) : breadcrumb.length === 0 ? (
          <p className="text-xs text-muted-foreground">{t('assetIsRoot')}</p>
        ) : (
          <div className="flex flex-wrap items-center gap-1 text-sm">
            {breadcrumb.map((a) => (
              <span key={a.id} className="flex items-center gap-1">
                <Link to={`/assets/${a.token}`} className="text-primary hover:underline">
                  {a.name || a.token}
                </Link>
                <ChevronRight size={13} className="text-muted-foreground" />
              </span>
            ))}
            <span className="text-muted-foreground">{assetToken}</span>
          </div>
        )}
      </div>

      {/* Move it. Placing under a parent REPLACES whatever parent it had, so there
          is one control for "place" and one for "detach", not three. */}
      <div className="space-y-4 rounded-lg border border-border bg-muted/40 p-4">
        <FormField label={t('assetParentLabel')} description={t('assetParentHint')}>
          <Combobox
            options={candidates.map((a) => ({ value: a.token, label: a.name || a.token }))}
            value={parentToken}
            onChange={setParentToken}
            placeholder={t('assetParentPlaceholder')}
          />
        </FormField>
        <div className="flex gap-2">
          <Button onClick={place} loading={submitting} disabled={submitting}>
            <CornerLeftUp size={14} /> {t('assetSetParentAction')}
          </Button>
          <Button variant="outline" onClick={detach} disabled={submitting}>
            <Unlink size={14} /> {t('assetClearParentAction')}
          </Button>
        </div>
      </div>

      {/* What sits directly under it. */}
      <div className="space-y-2">
        <div className="text-sm font-medium text-foreground">{t('assetChildrenTitle')}</div>
        {childrenQuery.loading ? (
          <LoadingState description={t('assetChildrenLoading')} />
        ) : childrenQuery.error ? (
          <ErrorState description={childrenQuery.error} />
        ) : children.length === 0 ? (
          <EmptyState description={t('assetNoChildren')} />
        ) : (
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>{t('common:colToken')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('common:colName')}</DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {children.map((c) => (
                <DataTableRow key={c.id}>
                  <DataTableCell>
                    <Link to={`/assets/${c.token}`} className="text-primary hover:underline">
                      {c.token}
                    </Link>
                  </DataTableCell>
                  <DataTableCell className="text-muted-foreground">{c.name || '—'}</DataTableCell>
                </DataTableRow>
              ))}
            </DataTableBody>
          </DataTable>
        )}
      </div>
    </div>
  );
}
