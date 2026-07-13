// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The rule-health view rendered in a device-profile detail tab (ADR-051 slice 7c): the
// "Observe" companion to the Detection Rules authoring tab. It answers "is my rule firing?"
// with two joined surfaces — a per-rule health table (status / last-fired / lifetime fire
// count, from the durable ruleHealth query over the profile's ACTIVE version) and a live
// detection feed (the detectionStream subscription, every firing raised or resolved). The
// table is the source of truth (durable counts); the feed is a best-effort live tail, so on
// a socket reconnect we re-run the query to pick up any firing the gap advanced.

import { useEffect, useRef, useState } from 'react';
import { Badge } from '@/components/ui/badge';
import {
  DataTable,
  DataTableHead,
  DataTableHeaderCell,
  DataTableBody,
  DataTableRow,
  DataTableCell,
} from '@/components/ui/data-table';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { useQuery } from '@/lib/hooks/use-query';
import { useReload } from '@/routes/common';
import { useDetectionStream } from '@/lib/hooks/use-detection-stream';
import { listRuleHealth, type DetectionEvent } from '@/lib/api/event-processing';

const Dash = () => <span className="text-muted-foreground">—</span>;

// Cap the live feed so a long-lived tab can't grow unbounded; the durable counts live in
// the table, the feed is only a recent tail.
const FEED_CAP = 50;

type FeedItem = DetectionEvent & { key: number };

const fmtTime = (iso: string | null | undefined): string => {
  if (!iso) return '';
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleString();
};

function StatusBadge({ status }: { status: string }) {
  if (status === 'ACTIVE') return <Badge variant="success">Active</Badge>;
  if (status === 'COMPILE_ERROR') return <Badge variant="destructive">Compile error</Badge>;
  return <Badge variant="secondary">{status}</Badge>;
}

function EdgeBadge({ edge }: { edge: string }) {
  return edge === 'resolved' ? (
    <Badge variant="secondary">resolved</Badge>
  ) : (
    <Badge variant="destructive">raised</Badge>
  );
}

export function RuleHealthPanel({ profileToken }: { profileToken: string }) {
  const [version, reload] = useReload();
  const { data, loading, error } = useQuery(() => listRuleHealth(profileToken), [profileToken, version]);

  // Live feed: a capped, newest-first tail of firings. A monotonic counter gives each row a
  // stable React key (two firings can share every wire field — same rule/edge/series/time).
  const [feed, setFeed] = useState<FeedItem[]>([]);
  const nextKey = useRef(0);
  // A coalescing timer so the durable table (last-fired / fire counts) refreshes shortly after
  // a burst of firings, instead of sitting frozen next to a visibly-updating feed (Fable 7c LOW).
  // Idle when nothing fires — no blind polling.
  const refreshTimer = useRef<number | null>(null);
  useEffect(() => () => {
    if (refreshTimer.current != null) window.clearTimeout(refreshTimer.current);
  }, []);
  const { status } = useDetectionStream(
    profileToken,
    (ev) => {
      setFeed((prev) => [{ ...ev, key: nextKey.current++ }, ...prev].slice(0, FEED_CAP));
      if (refreshTimer.current == null) {
        refreshTimer.current = window.setTimeout(() => {
          refreshTimer.current = null;
          reload();
        }, 4000);
      }
    },
    // A reconnect may have missed firings; refresh the durable counts.
    reload,
  );

  const rules = data ?? [];

  return (
    <div className="space-y-6">
      <div className="space-y-3">
        <p className="max-w-prose text-sm text-muted-foreground">
          Per-rule health for this profile's active published version. A rule fires against live telemetry
          only after the profile is published.
        </p>
        {loading && !data ? (
          <LoadingState description="Loading rule health…" />
        ) : error ? (
          <ErrorState description={error} />
        ) : rules.length === 0 ? (
          <p className="rounded-md border border-dashed px-4 py-8 text-center text-sm text-muted-foreground">
            No published detection rules. Author rules in the Detection Rules tab and publish the profile.
          </p>
        ) : (
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>Rule</DataTableHeaderCell>
              <DataTableHeaderCell>Status</DataTableHeaderCell>
              <DataTableHeaderCell>Last fired</DataTableHeaderCell>
              <DataTableHeaderCell className="text-right">Fires</DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {rules.map((r) => (
                <DataTableRow key={r.ruleId}>
                  <DataTableCell>
                    <span className="font-medium">{r.name}</span>
                    {r.status === 'COMPILE_ERROR' && r.message && (
                      <span className="mt-0.5 block text-xs text-destructive">{r.message}</span>
                    )}
                  </DataTableCell>
                  <DataTableCell>
                    <StatusBadge status={r.status} />
                  </DataTableCell>
                  <DataTableCell>
                    {r.lastFiredAt ? (
                      <span className="tabular-nums">
                        {fmtTime(r.lastFiredAt)}
                        {r.lastSignal && <span className="ml-1 text-muted-foreground">({r.lastSignal})</span>}
                      </span>
                    ) : (
                      <Dash />
                    )}
                  </DataTableCell>
                  <DataTableCell className="text-right tabular-nums">{r.fireCount}</DataTableCell>
                </DataTableRow>
              ))}
            </DataTableBody>
          </DataTable>
        )}
      </div>

      <div className="space-y-3">
        <div className="flex items-center gap-2">
          <h3 className="text-sm font-medium">Live detections</h3>
          <FeedStatus status={status} />
        </div>
        {feed.length === 0 ? (
          <p className="rounded-md border border-dashed px-4 py-6 text-center text-sm text-muted-foreground">
            Waiting for detections… firings appear here as rules trigger.
          </p>
        ) : (
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>Time</DataTableHeaderCell>
              <DataTableHeaderCell>Rule</DataTableHeaderCell>
              <DataTableHeaderCell>Edge</DataTableHeaderCell>
              <DataTableHeaderCell>Device</DataTableHeaderCell>
              <DataTableHeaderCell className="text-right">Value</DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {feed.map((f) => (
                <DataTableRow key={f.key}>
                  <DataTableCell className="tabular-nums text-muted-foreground">{fmtTime(f.occurredTime)}</DataTableCell>
                  <DataTableCell>
                    <span className="font-medium">{f.ruleToken}</span>
                    <span className="ml-1 text-xs text-muted-foreground">{f.kind}</span>
                  </DataTableCell>
                  <DataTableCell>
                    <EdgeBadge edge={f.edge} />
                  </DataTableCell>
                  <DataTableCell className="font-mono text-xs">{f.series}</DataTableCell>
                  <DataTableCell className="text-right tabular-nums">
                    {f.value == null ? <Dash /> : f.value}
                  </DataTableCell>
                </DataTableRow>
              ))}
            </DataTableBody>
          </DataTable>
        )}
      </div>
    </div>
  );
}

function FeedStatus({ status }: { status: 'connecting' | 'live' | 'reconnecting' }) {
  const label = status === 'live' ? 'live' : status === 'connecting' ? 'connecting…' : 'reconnecting…';
  const color =
    status === 'live' ? 'bg-success' : status === 'connecting' ? 'bg-muted-foreground' : 'bg-destructive';
  return (
    <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
      <span className={`h-2 w-2 rounded-full ${color}`} />
      {label}
    </span>
  );
}
