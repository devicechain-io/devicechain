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

import { useTranslation } from 'react-i18next';
import { VersionsPanel as SharedVersionsPanel } from '@/components/VersionsPanel';
import {
  listDeviceProfileVersions,
  publishDeviceProfile,
  rollbackDeviceProfile,
} from '@/lib/api/device-management';

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

  return (
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
      publish={publishDeviceProfile}
      rollback={rollbackDeviceProfile}
      onChanged={onChanged}
    />
  );
}
