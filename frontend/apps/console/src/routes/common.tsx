// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Small shared helpers for the admin console pages (ADR-033). Kept deliberately
// light — the admin console is a slim CRUD surface over the admin API.

import {
  useCallback,
  useState,
  type KeyboardEvent,
  type ReactNode,
  type TextareaHTMLAttributes,
} from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { ArrowLeft, CheckCircle2, CircleSlash } from 'lucide-react';
import { GraphQLRequestError } from '@devicechain/client';
import { Badge } from '@/components/ui/badge';
import { cn } from '@/lib/utils';

// useReload returns a version counter and a bump function; pass the version in a
// useQuery deps array to refetch a list after a mutation.
export function useReload(): [number, () => void] {
  const [version, setVersion] = useState(0);
  const reload = useCallback(() => setVersion((v) => v + 1), []);
  return [version, reload];
}

// errMessage extracts a human-readable message from a thrown error. GraphQL errors
// carry the server's message; a plain Error carries its own (e.g. a non-GraphQL
// endpoint like the branding-logo upload surfaces the server's 4xx/503 body); only a
// truly opaque throwable falls back to the generic transport failure.
export function errMessage(err: unknown): string {
  if (err instanceof GraphQLRequestError) return err.message;
  if (err instanceof Error && err.message) return err.message;
  return 'Could not reach the server.';
}

// typeCountLabel renders a device-type adoption count (ADR-045): "unused" at zero,
// else a pluralized "N type(s)". Shared by the profiles list column and the
// type-picker options so the two stay in lockstep.
export function typeCountLabel(count: number): string {
  return count === 0 ? 'unused' : `${count} type${count === 1 ? '' : 's'}`;
}

// rowLinkProps makes a whole table row behave like a link to a detail page:
// clickable, keyboard-focusable, and activatable with Enter/Space. Spread it onto
// a DataTableRow whose cells are plain content; a nested interactive control (e.g.
// a per-row delete button) is fine as long as it stops propagation of both click
// and keydown so it doesn't also trigger the row's navigation.
export function rowLinkProps(onActivate: () => void) {
  return {
    role: 'button' as const,
    tabIndex: 0,
    className: 'cursor-pointer',
    onClick: onActivate,
    onKeyDown: (e: KeyboardEvent) => {
      if (e.key === 'Enter' || e.key === ' ') {
        e.preventDefault();
        onActivate();
      }
    },
  };
}

// Textarea mirrors the Input primitive's styling for multi-line fields (tenant
// config JSON, role descriptions).
export function Textarea({ className, ...props }: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return (
    <textarea
      className={cn(
        'flex min-h-20 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50 font-mono',
        className,
      )}
      {...props}
    />
  );
}

// StatusBadge renders an enabled/disabled pill — green with a check when enabled,
// muted with a slash when not, so status reads at a glance from colour + icon.
export function StatusBadge({ enabled }: { enabled: boolean }) {
  const { t } = useTranslation('common');
  return enabled ? (
    <Badge variant="success" className="gap-1">
      <CheckCircle2 size={12} /> {t('enabled')}
    </Badge>
  ) : (
    <Badge variant="outline" className="gap-1 text-muted-foreground">
      <CircleSlash size={12} /> {t('disabled')}
    </Badge>
  );
}

// Governance rate-limit dimensions (ADR-065) arrive from the server with English
// display labels + units (the core governance fetcher hardcodes "Ingest",
// "events/sec", …). The stable `dimension.name` token ('ingest'/'outbound'/
// 'ai-inference') is the localization key: map it to a `common` catalog entry so
// the tenant/tier governance screens render a localized label rather than
// interpolating raw English into an otherwise-translated template. An unknown
// dimension falls back to the server-supplied string (visible, not blank).
const DIMENSION_LABEL_KEY: Record<string, string> = {
  ingest: 'common:dimensionIngest',
  outbound: 'common:dimensionOutbound',
  'ai-inference': 'common:dimensionAiDrafting',
};
const DIMENSION_UNIT_KEY: Record<string, string> = {
  ingest: 'common:unitEventsPerSec',
  outbound: 'common:unitCallsPerSec',
  'ai-inference': 'common:unitRequestsPerMin',
};

export function dimensionLabel(t: TFunction, name: string, fallback: string): string {
  return DIMENSION_LABEL_KEY[name] ? t(DIMENSION_LABEL_KEY[name]) : fallback;
}

export function dimensionUnit(t: TFunction, name: string, fallback: string): string {
  return DIMENSION_UNIT_KEY[name] ? t(DIMENSION_UNIT_KEY[name]) : fallback;
}

// BackLink is the small "← Section" link shown atop detail/new pages, returning
// the user to the resource's list.
export function BackLink({ to, children }: { to: string; children: ReactNode }) {
  return (
    <Link to={to} className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
      <ArrowLeft size={14} /> {children}
    </Link>
  );
}

