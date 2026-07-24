// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { Monitor, Moon, Sun } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { useTheme } from '@/components/ThemeProvider';
import { cn } from '@/lib/utils';

const OPTIONS = [
  { value: 'light', icon: Sun, label: 'light' },
  { value: 'dark', icon: Moon, label: 'dark' },
  { value: 'system', icon: Monitor, label: 'system' },
] as const;

/** Three-way segmented light / dark / system theme switch. */
export function ThemeToggle() {
  const { theme, setTheme } = useTheme();
  const { t } = useTranslation('theme');
  return (
    <div className="flex items-center gap-1 rounded-md border border-border bg-background p-0.5">
      {OPTIONS.map(({ value, icon: Icon, label }) => (
        <button
          key={value}
          type="button"
          onClick={() => setTheme(value)}
          aria-label={t(label)}
          title={t(label)}
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