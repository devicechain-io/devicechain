// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { cn } from '@/lib/utils';

export function DataTable({ children, className }: { children: React.ReactNode; className?: string }) {
  return (
    <div className={cn('bg-card rounded-lg border border-border overflow-hidden', className)}>
      <table className="w-full">
        {children}
      </table>
    </div>
  );
}

export function DataTableHead({ children }: { children: React.ReactNode }) {
  return (
    <thead>
      <tr className="border-b border-border">
        {children}
      </tr>
    </thead>
  );
}

export function DataTableHeaderCell({ children, className }: { children: React.ReactNode; className?: string }) {
  return (
    <th className={cn('text-left px-4 py-3 text-xs font-medium text-muted-foreground uppercase tracking-wider', className)}>
      {children}
    </th>
  );
}

export function DataTableBody({ children }: { children: React.ReactNode }) {
  return (
    <tbody className="divide-y divide-border">
      {children}
    </tbody>
  );
}

export function DataTableRow({ children, className, ...props }: React.HTMLAttributes<HTMLTableRowElement>) {
  return (
    <tr className={cn('hover:bg-muted/50 transition-colors', className)} {...props}>
      {children}
    </tr>
  );
}

export function DataTableCell({ children, className }: { children: React.ReactNode; className?: string }) {
  return (
    <td className={cn('px-4 py-3 text-sm', className)}>
      {children}
    </td>
  );
}