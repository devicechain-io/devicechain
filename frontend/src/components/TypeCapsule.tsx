// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Capsules used across the console: TypeCapsule renders a registry type with its
// editable appearance (icon + background/text/border colors), falling back to a
// neutral pill when no appearance is set; TokenCapsule is the monospace id pill.

import { cn } from '@/lib/utils';
import { typeIcon } from '@/lib/type-icons';

export interface TypeAppearance {
  token: string;
  name?: string | null;
  icon?: string | null;
  backgroundColor?: string | null;
  foregroundColor?: string | null;
  borderColor?: string | null;
}

export function TypeCapsule({
  appearance,
  className,
}: {
  appearance: TypeAppearance;
  className?: string;
}) {
  const Icon = typeIcon(appearance.icon);
  const styled = Boolean(
    appearance.backgroundColor || appearance.foregroundColor || appearance.borderColor,
  );
  const style = styled
    ? {
        backgroundColor: appearance.backgroundColor ?? undefined,
        color: appearance.foregroundColor ?? undefined,
        borderColor: appearance.borderColor ?? 'transparent',
      }
    : undefined;

  return (
    <span
      className={cn(
        'inline-flex items-center gap-1 rounded-md border px-2 py-0.5 text-xs font-medium',
        !styled && 'border-transparent bg-secondary text-secondary-foreground',
        className,
      )}
      style={style}
    >
      {Icon && <Icon size={12} className="shrink-0" />}
      {appearance.name || appearance.token}
    </span>
  );
}

export function TokenCapsule({ token, className }: { token: string; className?: string }) {
  return (
    <span
      className={cn(
        'inline-flex items-center rounded-md border border-border bg-muted px-2 py-0.5 font-mono text-xs text-muted-foreground',
        className,
      )}
    >
      {token}
    </span>
  );
}
