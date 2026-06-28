// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Small shared helpers for the admin console pages (ADR-033). Kept deliberately
// light — the admin console is a slim CRUD surface over the admin API.

import { useCallback, useState, type ReactNode, type TextareaHTMLAttributes } from 'react';
import { Link } from 'react-router-dom';
import { ArrowLeft } from 'lucide-react';
import { GraphQLRequestError } from '@/lib/graphql/client';
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

// parseTokens splits a free-text token field (space- or comma-separated) into a
// clean, de-duplicated list — used for role-token and authority inputs.
export function parseTokens(input: string): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const raw of input.split(/[\s,]+/)) {
    const t = raw.trim();
    if (t && !seen.has(t)) {
      seen.add(t);
      out.push(t);
    }
  }
  return out;
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

// StatusBadge renders an enabled/disabled pill.
export function StatusBadge({ enabled }: { enabled: boolean }) {
  return (
    <Badge variant={enabled ? 'outline' : 'destructive'}>{enabled ? 'Enabled' : 'Disabled'}</Badge>
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

// AdminCard is a titled section used to frame create/edit forms and detail panels.
export function AdminCard({
  title,
  description,
  children,
}: {
  title: string;
  description?: string;
  children: ReactNode;
}) {
  return (
    <div className="rounded-lg border border-border bg-card p-5">
      <div className="mb-4">
        <h3 className="text-sm font-semibold text-foreground">{title}</h3>
        {description && <p className="mt-0.5 text-xs text-muted-foreground">{description}</p>}
      </div>
      {children}
    </div>
  );
}
