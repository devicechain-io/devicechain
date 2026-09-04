// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Self-service tenant default-language editor (ADR-066 sub-workstream d). A tenant
// admin (locale:write) picks the language their console opens in for members who have
// not chosen one for themselves.
//
// It sits beside Branding and Map rather than on the operator's tenant page, and is
// gated on its OWN authority rather than branding:write, because this one value
// re-languages the console for every member of the tenant who has not chosen
// otherwise — a different kind of act from restyling a logo, plausibly owned by
// different people.
//
// 🔴 THIS SCREEN IS RUNG 2 OF FOUR, AND THE COPY SAYS SO. The console resolves an
// explicit user choice first, then this tenant default, then the browser's advertised
// languages, then English. Someone setting this needs to know it will not move a
// colleague who has already picked a language from the switcher — the alternative is a
// support question ("I set the tenant to Spanish and Dana still sees English") that the
// screen could have answered.
//
// The editor writes the RAW override (tenant.localeOverride; "inherit" = clear the
// column) and writes the freshly-resolved tenant straight into the tenant cache, so
// TenantProvider's effect re-applies the new default without a refetch.

import { useEffect, useState } from 'react';
import { Languages } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { hasAuthority } from '@devicechain/client';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { FormField } from '@/components/ui/form-field';
import { HintText } from '@/components/ui/hint-text';
import { ErrorBanner } from '@/components/ui/error-banner';
import { LoadingState } from '@/components/ui/loading-state';
import { SegmentedControl } from '@/components/ui/segmented-control';
import { useToast } from '@/components/ui/toast';
import { useAuth } from '@/auth/AuthProvider';
import { useCurrentTenant, useSetCurrentTenant } from '@/auth/TenantProvider';
import { SUPPORTED_LOCALES } from '@/i18n/config';
import { setTenantLocale } from '@/lib/api/user-management';
import { errMessage } from '@/routes/common';

// The authority name shown in the permission-denied message, in a font-mono span — a
// technical identifier, never localized (mirrors how a token/id is displayed).
const LOCALE_WRITE_AUTHORITY = 'locale:write';

// The sentinel for "this tenant inherits the instance default". A radio group cannot
// be cleared, so "inherit" is a segment rather than an absence — and this value can
// never collide with a real selection, because a BCP-47 primary subtag is at most
// three letters and this is seven.
const INHERIT = 'inherit';

export default function LocalePage() {
  const { t } = useTranslation('locale');
  const { claims } = useAuth();
  const canWrite = hasAuthority(claims, 'locale:write');
  const tenant = useCurrentTenant();

  if (!canWrite) {
    return (
      <PageShell title={t('title')} description={t('description')}>
        <p className="text-sm text-muted-foreground">
          {t('noPermissionPrefix')}
          <span className="font-mono"> {LOCALE_WRITE_AUTHORITY} </span>
          {t('noPermissionSuffix')}
        </p>
      </PageShell>
    );
  }

  // 🔴 Gate on the TENANT, not on the override. Unlike branding and basemap, a null
  // override is a legitimate steady state here (it is what "inherit" looks like), so
  // it cannot double as the not-yet-fetched signal the way `brandingOverride` does.
  // Seeding the form from a tenant that has not landed would show "Inherit" to a
  // tenant that has a language set, and a Save on that would clear it.
  if (!tenant) {
    return (
      <PageShell title={t('title')} description={t('description')}>
        <LoadingState description={t('loading')} />
      </PageShell>
    );
  }
  return <LocaleEditor key={tenant.token} />;
}

function LocaleEditor() {
  const { t } = useTranslation('locale');
  const applyTenant = useSetCurrentTenant();
  const tenant = useCurrentTenant();
  const { toast } = useToast();

  const override = tenant?.localeOverride ?? null;
  const [choice, setChoice] = useState<string>(() => override ?? INHERIT);
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // Re-seed a pristine form when a newer override arrives (a stale cached tenant, or
  // another admin's change); never once the user has started editing.
  const [dirty, setDirty] = useState(false);

  useEffect(() => {
    if (!dirty) setChoice(override ?? INHERIT);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [override]);

  const change = (next: string) => {
    setDirty(true);
    setChoice(next);
  };

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      const updated = await setTenantLocale(choice === INHERIT ? null : choice);
      applyTenant(updated);
      setDirty(false);
      toast(t('savedToast'));
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  // 🔴 `tenant.locale` is the EFFECTIVE value, which already includes this tenant's own
  // override — so it is only the instance default when this tenant stores none. The
  // hint is therefore gated on the STORED override being absent, not on the selection
  // being "inherit". Without that gate, a tenant with its own language who switches the
  // selection to Inherit would be told "Inheriting the instance default: <its own
  // language>", which is wrong twice over: that is not the instance default, and saving
  // will not keep it. Same trap the basemap editor documents; same answer.
  const inherited = tenant?.locale ?? null;
  const inheritedLabel = SUPPORTED_LOCALES.find((l) => l.code === inherited)?.label ?? inherited;
  const showInherited = !override && !!inherited;

  return (
    <PageShell title={t('title')} description={t('description')}>
      <div className="max-w-2xl space-y-6">
        {formError && <ErrorBanner message={formError} />}

        <FormField label={t('defaultLanguage')} description={t('defaultLanguageHelp')}>
          <SegmentedControl
            ariaLabel={t('defaultLanguage')}
            value={choice}
            onValueChange={change}
            tone="loose"
            size="md"
            leading={<Languages className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />}
            options={[
              { value: INHERIT, label: t('inheritOption') },
              // Endonyms are never themselves translated, so the label comes from the
              // locale registry rather than from a catalog. The badge is decorative
              // beside it, exactly as in the switcher.
              ...SUPPORTED_LOCALES.map(({ code, label, badge }) => ({
                value: code,
                title: label,
                label: (
                  <>
                    <span
                      aria-hidden
                      className="mr-1 rounded bg-muted px-1 py-0.5 font-mono text-[10px] font-semibold leading-none"
                    >
                      {badge}
                    </span>
                    {label}
                  </>
                ),
              })),
            ]}
          />
        </FormField>

        {showInherited && (
          <div data-testid="locale-inheriting">
            <HintText size="md">{t('inheriting', { locale: inheritedLabel })}</HintText>
          </div>
        )}

        {/* The precedence note is on the screen rather than in a doc, because the
            question it answers ("why didn't my colleague's console change?") is asked
            at exactly this moment. */}
        <HintText size="md">{t('precedenceNote')}</HintText>

        <div className="flex gap-2">
          <Button onClick={submit} loading={busy}>
            {t('save')}
          </Button>
        </div>
      </div>
    </PageShell>
  );
}
