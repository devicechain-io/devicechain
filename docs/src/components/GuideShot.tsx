// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import React, {useEffect, useRef, useState} from 'react';
import {translate} from '@docusaurus/Translate';
import styles from './GuideShot.module.css';
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
  return <Zoomable src={src} alt={alt} width={shot.width} height={shot.height} />;
}

/**
 * A native <dialog> lightbox (no dependency). showModal() gives the focus trap, inert
 * background, Escape-to-close and focus return to the trigger. The capture is served as a
 * single full-resolution file, so the enlarged view uses the same src. Nothing here touches
 * the DOM until an event or effect runs, so server rendering is unaffected.
 */
function Zoomable({src, alt, width, height}: {src: string; alt: string; width: number; height: number}): React.JSX.Element {
  const [open, setOpen] = useState(false);
  const [actual, setActual] = useState(false);
  const dialogRef = useRef<HTMLDialogElement>(null);

  useEffect(() => {
    const dlg = dialogRef.current;
    if (open && dlg && !dlg.open) {
      dlg.showModal();
    }
  }, [open]);

  const close = () => {
    dialogRef.current?.close();
  };
  const onClosed = () => {
    setOpen(false);
    setActual(false);
  };

  const enlarge = translate({id: 'guideShot.enlarge', message: 'Enlarge image', description: 'Prefix of the accessible name of a guide screenshot button; the image description follows'});
  const closeLabel = translate({id: 'guideShot.close', message: 'Close', description: 'Button that closes the enlarged guide screenshot'});
  const toggleLabel = translate({id: 'guideShot.toggleSize', message: 'Toggle between fitted and full size', description: 'Accessible hint on the enlarged guide screenshot'});

  return (
    <>
      <button type="button" className={styles.trigger} aria-label={`${enlarge}: ${alt}`} aria-haspopup="dialog" onClick={() => setOpen(true)}>
        <img src={src} alt="" aria-hidden="true" width={width} height={height} loading="lazy" decoding="async" className={styles.thumb} />
        <span className={styles.badge} aria-hidden="true">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round">
            <circle cx="11" cy="11" r="7" />
            <path d="M21 21l-4.3-4.3M11 8v6M8 11h6" />
          </svg>
        </span>
      </button>
      {open && (
        <dialog
          ref={dialogRef}
          className={styles.dialog}
          aria-label={alt}
          onClose={onClosed}
          onClick={(e) => {
            if (e.target === e.currentTarget) close();
          }}
        >
          <button type="button" className={styles.close} onClick={close}>
            {closeLabel}
          </button>
          <div
            className={styles.stage}
            onClick={(e) => {
              if (e.target === e.currentTarget) close();
            }}
          >
            <img
              src={src}
              alt={alt}
              width={width}
              height={height}
              title={toggleLabel}
              className={actual ? `${styles.image} ${styles.imageActual}` : styles.image}
              onClick={() => setActual((v) => !v)}
            />
          </div>
        </dialog>
      )}
    </>
  );
}
