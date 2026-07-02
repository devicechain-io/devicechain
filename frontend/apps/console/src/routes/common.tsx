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

// errMessage extracts a human-readable message from a thrown error (GraphQL
// errors carry the server's message; anything else is a transport failure).
export function errMessage(err: unknown): string {
  if (err instanceof GraphQLRequestError) return err.message;
  return 'Could not reach the server.';
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
  return enabled ? (
    <Badge variant="success" className="gap-1">
      <CheckCircle2 size={12} /> Enabled
    </Badge>
  ) : (
    <Badge variant="outline" className="gap-1 text-muted-foreground">
      <CircleSlash size={12} /> Disabled
    </Badge>
  );
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

