// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A provisioning profile: what it registers, how many devices it has, and the operator controls
// for a fleet that registers itself — rotate the secret, revoke the devices it registered, delete.
// The edit form below the summary sends only the fields that changed.

import '@/routes/trust-anchors/catalog';
import { useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { KeyRound, ShieldOff, Trash2 } from 'lucide-react';
import { PageShell } from '@/components/ui/page-shell';
import { CopyToken } from '@/components/ui/copy-token';
import { SectionPanel } from '@/components/ui/section-panel';
import { Button } from '@/components/ui/button';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { ErrorBanner } from '@/components/ui/error-banner';
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useQuery } from '@/lib/hooks/use-query';
import { useAuth } from '@/auth/AuthProvider';
import { mayWrite } from '@/lib/scope';
import { errMessage, useReload } from '@/routes/common';
import {
  countEarmarkedDevices,
  deleteProvisioningProfile,
  getProvisioningProfile,
  revokeProvisionedDevices,
  rotateProvisioningProfileSecret,
  type ProvisioningProfile,
} from '@/lib/api/provisioning';
import { ProvisioningProfileForm } from './ProvisioningProfileForm';
import { ProfileBadges } from './ProfileBadges';
import { SecretReveal, type RevealedSecret } from './SecretReveal';
import { DERIVED_TOKEN_PREFIX } from '@/routes/trust-anchors/anchorRules';

export default function ProvisioningProfileDetailPage() {
  const { t } = useTranslation('provisioning');
  const { token: rawToken } = useParams<{ token: string }>();
  const token = decodeURIComponent(rawToken ?? '');
  const [version, reload] = useReload();
  const { data: profile, loading, error } = useQuery(() => getProvisioningProfile(token), [token, version]);

  // Only the first load takes the page over; a reload after a rotate keeps the page (and the
  // open reveal dialog) in place.
  if (loading && !profile) {
    return (
      <PageShell title={token} banner="devices">
        <LoadingState description={t('loadingProfile')} />
      </PageShell>
    );
  }
  if (error && !profile) {
    return (
      <PageShell title={token} banner="devices">
        <ErrorState description={error} />
      </PageShell>
    );
  }
  if (!profile) {
    return (
      <PageShell title={token} banner="devices">
        <ErrorState description={t('notFound', { token })} />
      </PageShell>
    );
  }
  // Keyed by token so no state survives a move to another profile.
  return <ProfileDetail key={token} profile={profile} reload={reload} />;
}

function ProfileDetail({ profile, reload }: { profile: ProvisioningProfile; reload: () => void }) {
  const { t } = useTranslation('provisioning');
  const navigate = useNavigate();
  const { toast } = useToast();
  const confirm = useConfirm();
  const { claims } = useAuth();
  const canWrite = mayWrite(claims, 'device:write');
  const [revealed, setRevealed] = useState<RevealedSecret | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const { data: earmarked } = useQuery(() => countEarmarkedDevices(profile.token), [profile.token, profile.updatedAt]);

  const rotate = async () => {
    setActionError(null);
    if (
      !(await confirm({
        title: t('rotateTitle'),
        description: t('rotateConfirm', { token: profile.token, count: profile.provisionedDeviceCount }),
        confirmLabel: t('rotateAction'),
      }))
    )
      return;
    setBusy(true);
    try {
      const { profile: updated, issuedSecret } = await rotateProvisioningProfileSecret(profile.token);
      if (issuedSecret === null) {
        // Only a certificate profile answers with no secret, and it is not offered a rotation.
        setActionError(t('trustAnchors:rotateNoSecret'));
        return;
      }
      setRevealed({
        profileToken: updated.token,
        provisionKey: updated.provisionKey,
        secret: issuedSecret,
        rotated: true,
      });
      reload();
    } catch (err) {
      setActionError(errMessage(err));
      toast(errMessage(err), 'error');
    } finally {
      setBusy(false);
    }
  };

  const revoke = async () => {
    setActionError(null);
    // The count is read fresh for the confirmation: devices may have registered since the page loaded.
    let count = profile.provisionedDeviceCount;
    try {
      count = (await getProvisioningProfile(profile.token))?.provisionedDeviceCount ?? count;
    } catch {
      // The loaded count still states the scale; the server revokes whatever exists.
    }
    if (
      !(await confirm({
        title: t('revokeTitle'),
        description: t('revokeConfirm', { token: profile.token, count }),
        confirmLabel: t('revokeAction'),
      }))
    )
      return;
    setBusy(true);
    try {
      const n = await revokeProvisionedDevices(profile.token);
      toast(t('revokedToast', { count: n }));
      reload();
    } catch (err) {
      setActionError(errMessage(err));
      toast(errMessage(err), 'error');
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    setActionError(null);
    if (
      !(await confirm({
        title: t('deleteTitle'),
        description: t('deleteConfirm', { token: profile.token }),
        confirmLabel: t('delete'),
      }))
    )
      return;
    try {
      await deleteProvisioningProfile(profile.token);
      toast(t('deletedToast', { token: profile.token }));
      navigate('/provisioning-profiles');
    } catch (err) {
      // The server's refusal says what to do first ("revoke the profile's devices first").
      setActionError(errMessage(err));
      toast(errMessage(err), 'error');
    }
  };

  const isCertificate = profile.trustAnchor != null;

  const filterLink = (param: string) =>
    `/devices?${param}=${encodeURIComponent(profile.token)}`;

  return (
    <PageShell
      title={profile.name || profile.token}
      titleAdornment={profile.name ? <CopyToken value={profile.token} /> : undefined}
      banner="devices"
      description={<ProfileBadges profile={profile} />}
      action={
        canWrite ? (
          <div className="flex flex-wrap items-center gap-2">
            {/* A certificate profile authenticates by certificate chain: no secret to rotate. */}
            {!isCertificate && (
              <Button variant="outline" size="sm" onClick={rotate} disabled={busy}>
                <KeyRound size={14} /> {t('rotateAction')}
              </Button>
            )}
            <Button variant="outline" size="sm" onClick={revoke} disabled={busy}>
              <ShieldOff size={14} /> {t('revokeAction')}
            </Button>
            <Button variant="destructive" size="sm" onClick={remove} disabled={busy}>
              <Trash2 size={14} /> {t('delete')}
            </Button>
          </div>
        ) : undefined
      }
    >
      <SecretReveal revealed={revealed} onClose={() => setRevealed(null)} />
      <div className="max-w-3xl space-y-4">
        {actionError && <ErrorBanner message={actionError} onDismiss={() => setActionError(null)} />}
        <SectionPanel title={t('summaryTitle')}>
          <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm">
            {profile.trustAnchor && (
              <>
                <dt className="text-muted-foreground">{t('trustAnchors:summaryAnchor')}</dt>
                <dd className="text-foreground">
                  <Link
                    className="text-primary underline-offset-2 hover:underline"
                    to={`/trust-anchors/${encodeURIComponent(profile.trustAnchor.token)}`}
                  >
                    {profile.trustAnchor.name || profile.trustAnchor.token}
                  </Link>
                </dd>
              </>
            )}
            <dt className="text-muted-foreground">{t('summaryRegistered')}</dt>
            <dd className="text-foreground tabular-nums">
              {profile.maxDevices == null
                ? t('registeredUncapped', { count: profile.provisionedDeviceCount })
                : t('registeredOfCap', { count: profile.provisionedDeviceCount, max: profile.maxDevices })}
              {profile.provisionedDeviceCount > 0 && (
                <>
                  {' '}
                  <Link className="text-primary underline-offset-2 hover:underline" to={filterLink('provisioningProfile')}>
                    {t('summaryViewRegistered')}
                  </Link>
                </>
              )}
            </dd>
            <dt className="text-muted-foreground">{t('summaryEarmarked')}</dt>
            <dd className="text-foreground tabular-nums">
              {earmarked ?? '—'}
              {(earmarked ?? 0) > 0 && (
                <>
                  {' '}
                  <Link className="text-primary underline-offset-2 hover:underline" to={filterLink('expectedProvisioningProfile')}>
                    {t('summaryViewEarmarked')}
                  </Link>
                </>
              )}
            </dd>
          </dl>
          <p className="mt-3 text-xs text-muted-foreground">
            {isCertificate ? t('trustAnchors:summaryNoteCertificate', { prefix: DERIVED_TOKEN_PREFIX }) : t('summaryNote')}
          </p>
        </SectionPanel>
        <SectionPanel>
          <ProvisioningProfileForm
            // Re-seeded when the stored profile changes (a save, a rotate), so the form is
            // measured against what the server now holds.
            key={`${profile.updatedAt}|${profile.secretIssued}`}
            entity={profile}
            readOnly={!canWrite}
            onSaved={(m) => {
              toast(m);
              reload();
            }}
          />
        </SectionPanel>
      </div>
    </PageShell>
  );
}
