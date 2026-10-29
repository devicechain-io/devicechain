// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Download } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { useToast } from '@/components/ui/toast';
import { getDeviceProfileThingModel } from '@/lib/api/device-management';
import '@/i18n/lazy/deviceProfiles';

// The media type the WoT Thing Description 1.1 Recommendation registers for its documents.
const TD_MEDIA_TYPE = 'application/td+json';

// thingModelFileName names the download after the profile and version, with the
// extension the WoT tooling recognises for a Thing Model.
export function thingModelFileName(profileToken: string, version: number): string {
  return `${profileToken}.v${version}.tm.jsonld`;
}

// ThingModelDownload saves one published version of a profile as a W3C Web of Things Thing
// Model. The model is fetched when asked for, not with the version list, so the list does
// not render a model for every version it shows.
export function ThingModelDownload({
  profileToken,
  version,
  latest,
}: {
  profileToken: string;
  version: number;
  /** The newest version number, which places this one in the newest-first list. */
  latest: number;
}) {
  const { t } = useTranslation('deviceProfiles');
  const { toast } = useToast();
  const [busy, setBusy] = useState(false);

  const download = async () => {
    setBusy(true);
    try {
      const model = await getDeviceProfileThingModel(profileToken, version, latest);
      const url = URL.createObjectURL(new Blob([model], { type: TD_MEDIA_TYPE }));
      const a = document.createElement('a');
      a.href = url;
      a.download = thingModelFileName(profileToken, version);
      a.click();
      URL.revokeObjectURL(url);
    } catch {
      toast(t('thingModelFailed', { version }), 'error');
    } finally {
      setBusy(false);
    }
  };

  return (
    <Button
      variant="ghost"
      size="sm"
      onClick={download}
      data-shot="thing-model-download"
      loading={busy}
      aria-label={t('thingModelDownloadLabel', { version })}
    >
      <Download size={14} /> {t('thingModelDownload')}
    </Button>
  );
}
