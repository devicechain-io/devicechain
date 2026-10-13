// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import React from 'react';
import useBaseUrl from '@docusaurus/useBaseUrl';
import useDocusaurusContext from '@docusaurus/useDocusaurusContext';
// Written by scripts/fetch-guide-assets.mjs from the pinned guide-assets commit.
// A missing file fails the webpack build, which is the point.
import manifestJson from '@site/static/img/guide/manifest.json';

interface Shot {
  locales: string[];
  width: number;
  height: number;
}
interface Manifest {
  schemaVersion: number;
  consoleVersion: string;
  shots: Record<string, Shot>;
}

const manifest = manifestJson as unknown as Manifest;
const FALLBACK_LOCALE = 'en';

export interface GuideShotProps {
  /** Capture id, e.g. "devices-list"; must exist in the fetched manifest. */
  id: string;
  /** Describes the screen for a reader who cannot see it. Required. */
  alt: string;
}

/**
 * A console screenshot from the guide-assets set. Uses the current locale's
 * capture, or English when that locale has none (zh-CN always does today).
 * An unknown id throws, which fails the docs build.
 */
export default function GuideShot({id, alt}: GuideShotProps): React.JSX.Element {
  const {i18n} = useDocusaurusContext();
  const shot = Object.prototype.hasOwnProperty.call(manifest.shots, id) ? manifest.shots[id] : undefined;
  if (!shot) {
    throw new Error(`GuideShot: unknown id "${id}" (not in the guide-assets manifest)`);
  }
  if (!alt || !alt.trim()) {
    throw new Error(`GuideShot: "${id}" needs alt text`);
  }
  const locale = shot.locales.includes(i18n.currentLocale) ? i18n.currentLocale : FALLBACK_LOCALE;
  if (!shot.locales.includes(locale)) {
    throw new Error(`GuideShot: "${id}" has no "${locale}" capture`);
  }
  const src = useBaseUrl(`/img/guide/${id}.${locale}.webp`);
  return <img src={src} alt={alt} width={shot.width} height={shot.height} loading="lazy" decoding="async" style={{maxWidth: '100%', height: 'auto'}} />;
}
