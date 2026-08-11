// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { Languages } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { SUPPORTED_LOCALES, setUserLocale } from '@/i18n/config';
import { SegmentedControl } from '@/components/ui/segmented-control';

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
  // Before i18next finishes resolving there is no language in effect; '' matches no
  // segment, which is the honest rendering of "not yet known".
  const active = i18n.resolvedLanguage ?? '';

  return (
    <SegmentedControl
      ariaLabel={t('languagePicker')}
      value={active}
      onValueChange={setUserLocale}
      fill
      className={className}
      leading={
        <Languages className="ml-1 size-3.5 shrink-0 text-muted-foreground" aria-hidden />
      }
      options={SUPPORTED_LOCALES.map(({ code, label, badge }) => ({
        value: code,
        title: label,
        label: (
          <>
            {/* The code badge is the quick visual anchor; it is decorative next to
                the authoritative endonym, so it is aria-hidden (the endonym beside it
                is the accessible name). Its chip background follows the segment's
                selection state, which the segment publishes as `data-state`. */}
            <span
              aria-hidden
              className="rounded bg-muted px-1 py-0.5 font-mono text-[10px] font-semibold leading-none group-data-[state=checked]:bg-primary-foreground/20"
            >
              {badge}
            </span>
            <span className="truncate">{label}</span>
          </>
        ),
      }))}
    />
  );
}
