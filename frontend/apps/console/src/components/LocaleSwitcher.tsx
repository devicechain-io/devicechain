// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { Languages } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { SUPPORTED_LOCALES, setUserLocale } from '@/i18n/config';
import { cn } from '@/lib/utils';

/**
 * Language picker — a chrome control (sibling of the ThemeToggle), not a form
 * field. Segmented over the locales the console ships (SUPPORTED_LOCALES), each
 * pill showing the language's code badge + its own endonym. Selecting a locale persists
 * it as an explicit user choice (ADR-066 precedence rung 1) via setUserLocale and
 * switches immediately, so it works anywhere it is mounted — the login footer AND
 * the in-app user menu. It reads the registry, so a new locale needs no change here.
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
      {SUPPORTED_LOCALES.map(({ code, label, badge }) => {
        const isActive = active === code;
        return (
          <button
            key={code}
            type="button"
            onClick={() => setUserLocale(code)}
            aria-pressed={isActive}
            title={label}
            className={cn(
              'flex h-7 flex-1 items-center justify-center gap-1.5 rounded-sm px-2 text-xs transition-colors',
              isActive
                ? 'bg-primary text-primary-foreground'
                : 'text-muted-foreground hover:text-foreground',
            )}
          >
            {/* The code badge is the quick visual anchor; it is decorative next to
                the authoritative endonym, so it is aria-hidden (title carries the
                name). Its chip background adapts to the pill's active state. */}
            <span
              aria-hidden
              className={cn(
                'rounded px-1 py-0.5 font-mono text-[10px] font-semibold leading-none',
                isActive ? 'bg-primary-foreground/20' : 'bg-muted',
              )}
            >
              {badge}
            </span>
            <span className="truncate">{label}</span>
          </button>
        );
      })}
    </div>
  );
}
