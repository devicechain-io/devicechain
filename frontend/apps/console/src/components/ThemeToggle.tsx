// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { Monitor, Moon, Sun } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { useTheme } from '@/components/ThemeProvider';
import { SegmentedControl } from '@/components/ui/segmented-control';

const OPTIONS = [
  { value: 'light', icon: Sun, label: 'light' },
  { value: 'dark', icon: Moon, label: 'dark' },
  { value: 'system', icon: Monitor, label: 'system' },
] as const;

type ThemeValue = (typeof OPTIONS)[number]['value'];

/** Three-way segmented light / dark / system theme switch. */
export function ThemeToggle() {
  const { theme, setTheme } = useTheme();
  const { t } = useTranslation('theme');
  return (
    <SegmentedControl<ThemeValue>
      ariaLabel={t('picker')}
      value={theme}
      onValueChange={setTheme}
      fill
      options={OPTIONS.map(({ value, icon: Icon, label }) => ({
        value,
        // Icon-only: the segment renders no text, so its accessible name comes from
        // these two rather than from its content. (Either alone would name it —
        // `title` is the last-resort source in the accessible-name algorithm — but
        // ariaLabel is the one that says so on purpose, and title is what a sighted
        // user hovers for.)
        label: <Icon className="size-3.5" />,
        ariaLabel: t(label),
        title: t(label),
      }))}
    />
  );
}
