// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { Monitor, Moon, Sun } from 'lucide-react';
import { useTheme } from '@/components/ThemeProvider';
import { cn } from '@/lib/utils';

const OPTIONS = [
  { value: 'light', icon: Sun, label: 'Light' },
  { value: 'dark', icon: Moon, label: 'Dark' },
  { value: 'system', icon: Monitor, label: 'System' },
] as const;

/** Three-way segmented light / dark / system theme switch. */
export function ThemeToggle() {
  const { theme, setTheme } = useTheme();
  return (
    <div className="flex items-center gap-1 rounded-md border border-border bg-background p-0.5">
      {OPTIONS.map(({ value, icon: Icon, label }) => (
        <button
          key={value}
          type="button"
          onClick={() => setTheme(value)}
          aria-label={label}
          title={label}
          className={cn(
            'flex h-7 flex-1 items-center justify-center rounded-sm transition-colors',
            theme === value
              ? 'bg-primary text-primary-foreground'
              : 'text-muted-foreground hover:text-foreground',
          )}
        >
          <Icon className="size-3.5" />
        </button>
      ))}
    </div>
  );
}