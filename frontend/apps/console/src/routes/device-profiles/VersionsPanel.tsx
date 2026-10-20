// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The Versions tab of a device profile (ADR-045 slice c/d). A profile is versioned
// as one unit: publishing freezes the current draft (all metric, command, and alarm
// definitions together) into an immutable version, and a device resolves the
// profile's active PUBLISHED version — so draft edits in the other tabs take effect
// only when published here. Rollback re-points the active version at an earlier one
// without touching the draft.
//
// The panel itself is @/components/VersionsPanel, shared with every other versioned
// tenant resource. What is left here is the profile's own sentences — the ones that
// say what a published version MEANS for a device — plus the "used by N types"
// warning, which no other resource has.

import { useCallback, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { hasAuthority, isScoped } from '@devicechain/client';
import { useAuth } from '@/auth/AuthProvider';
import { VersionsPanel as SharedVersionsPanel } from '@/components/VersionsPanel';
import { useConfirm } from '@/components/ui/confirm-dialog';
import {
  getCalculatedMetricPlan,
  listDeviceProfileVersions,
  publishDeviceProfile,
  rollbackDeviceProfile,
} from '@/lib/api/device-management';
import { BackfillSection } from './BackfillSection';
import { ThingModelDownload } from './ThingModelDownload';

// The authority string is a permission name, not user-facing prose, so it is hoisted
// out of the JSX tree — the i18n literal-string lint walks every node under a JSX
// element and cannot tell the two apart.
const DEVICE_WRITE = 'device:write';

export function VersionsPanel({
  profileToken,
  activeVersion,
  deviceTypeCount,
  onChanged,
}: {
  profileToken: string;
  /** The profile's currently-active published version, or null if never published. */
  activeVersion: number | null;
  /** How many device types adopt this profile — publishing affects all of them. */
  deviceTypeCount: number;
  /** Refresh the parent detail so the active-version badge updates after a change. */
  onChanged: () => void;
}) {
  const { t } = useTranslation(['deviceProfiles']);
  const { claims } = useAuth();
  const confirm = useConfirm();
  const [request, setRequest] = useState<number | null>(null);
  const handled = useCallback(() => setRequest(null), []);
  const offersBackfill = !isScoped(claims) && hasAuthority(claims, DEVICE_WRITE);

  // Publishing a version that ADDS a calculated metric offers to fill the history for it, once:
  // the answer is not remembered, and a version that adds none asks nothing. A failed look at the
  // plan asks nothing either; the Backfill button is still there.
  const publish = async (token: string, label?: string, description?: string) => {
    const version = await publishDeviceProfile(token, label, description);
    if (offersBackfill) void offerBackfill(token, version);
    return version;
  };
  const offerBackfill = async (token: string, version: number) => {
    try {
      const plan = await getCalculatedMetricPlan(token, version);
      const added = plan.calculated.filter((m) => m.newInVersion).map((m) => m.key);
      if (added.length === 0) return;
      const yes = await confirm({
        title: t('deviceProfiles:backfillPromptTitle'),
        description: t('deviceProfiles:backfillPromptBody', { count: added.length, keys: added.join(', ') }),
        confirmLabel: t('deviceProfiles:backfillPromptConfirm'),
        destructive: false,
      });
      if (yes) setRequest(version);
    } catch {
      // Not offering is the safe failure.
    }
  };

  return (
    <div className="space-y-6">
    <SharedVersionsPanel
      token={profileToken}
      activeVersion={activeVersion}
      authority={DEVICE_WRITE}
      copy={{
        notPublished: t('deviceProfiles:versionNotPublished'),
        publishExplain: t('deviceProfiles:versionPublishExplain'),
        notice:
          deviceTypeCount > 1
            ? t('deviceProfiles:versionUsedByWarning', { count: deviceTypeCount })
            : undefined,
        drawerTitle: t('deviceProfiles:versionPublishDrawerTitle'),
        drawerDescription: t('deviceProfiles:versionPublishDrawerDescription'),
        rollbackConfirm: (version) => t('deviceProfiles:versionRollbackConfirm', { version }),
      }}
      list={listDeviceProfileVersions}
      publish={publish}
      rollback={rollbackDeviceProfile}
      onChanged={onChanged}
      rowAction={(v, versions) => (
        <ThingModelDownload
          profileToken={profileToken}
          version={v.version}
          latest={versions[0]?.version ?? v.version}
        />
      )}
    />
    <BackfillSection
      profileToken={profileToken}
      activeVersion={activeVersion}
      request={request}
      onRequestHandled={handled}
    />
    </div>
  );
}
