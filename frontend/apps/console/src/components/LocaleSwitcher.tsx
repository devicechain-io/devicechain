// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { Languages } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { SUPPORTED_LOCALES, setUserLocale } from '@/i18n/config';
import { cn } from '@/lib/utils';

/**
 * Language picker — a chrome control (sibling of the ThemeToggle), not a form
 * field. Segmented over the locales the console ships (SUPPORTED_LOCALES).
 * Selecting a locale persists it as an explicit user choice (ADR-066 precedence
 * rung 1) via setUserLocale. While only English ships it renders a single pill;
 * it becomes a real choice when Spanish lands (sub-workstream c) with no change
 * here — it reads the registry.
 */
export function LocaleSwitcher({ className }: { className?: string }) {
  // Subscribing to useTranslation re-renders this control when the language
  // changes; i18n.resolvedLanguage is the locale actually in effect after the
  // supportedLngs/fallback resolution (never a raw unshipped browser value).
  // `t` (default `common` namespace) localizes the group label — the endonym pill
  // text and title are language names, which are never themselves translated.
  const { t, i18n } = useTranslation();
  const active = i18n.resolvedLanguage;

  return (
    <div
      className={cn(
        'flex items-center gap-1 rounded-md border border-border bg-background p-0.5',
        className,
      )}
      role="group"
      aria-label={t('languagePicker')}
    >
      <Languages className="ml-1 size-3.5 shrink-0 text-muted-foreground" aria-hidden />
      {SUPPORTED_LOCALES.map(({ code, label }) => (
        <button
          key={code}
          type="button"
          onClick={() => setUserLocale(code)}
          aria-pressed={active === code}
          title={label}
          className={cn(
            'flex h-7 items-center justify-center rounded-sm px-2 text-xs transition-colors',
            active === code
              ? 'bg-primary text-primary-foreground'
              : 'text-muted-foreground hover:text-foreground',
          )}
        >
          {label}
        </button>
      ))}
    </div>
  );
}
