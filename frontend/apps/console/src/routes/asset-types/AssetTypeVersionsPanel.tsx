// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The Versions tab of an asset type: publish the draft property contract, or roll the
// active one back. The panel is the shared one; what is here is the asset type's own
// sentences — the ones that say what a published contract does to its assets.

import { useTranslation } from 'react-i18next';
import { VersionsPanel } from '@/components/VersionsPanel';
import {
  listAssetTypeVersions,
  publishAssetType,
  rollbackAssetType,
} from '@/lib/api/assets';

// The authority string is a permission name, not user-facing prose, so it is hoisted
// out of the JSX tree — the i18n literal-string lint walks every node under a JSX
// element and cannot tell the two apart.
const DEVICE_WRITE = 'device:write';

export function AssetTypeVersionsPanel({
  assetTypeToken,
  activeVersion,
  onChanged,
}: {
  assetTypeToken: string;
  /** The type's currently-active published contract, or null if never published. */
  activeVersion: number | null;
  onChanged: () => void;
}) {
  const { t } = useTranslation(['entities']);

  return (
    <VersionsPanel
      token={assetTypeToken}
      activeVersion={activeVersion}
      authority={DEVICE_WRITE}
      copy={{
        notPublished: t('entities:assetVersionNotPublished'),
        publishExplain: t('entities:assetVersionPublishExplain'),
        drawerTitle: t('entities:assetVersionPublishDrawerTitle'),
        drawerDescription: t('entities:assetVersionPublishDrawerDescription'),
        rollbackConfirm: (version) => t('entities:assetVersionRollbackConfirm', { version }),
      }}
      list={listAssetTypeVersions}
      publish={publishAssetType}
      rollback={rollbackAssetType}
      onChanged={onChanged}
    />
  );
}
