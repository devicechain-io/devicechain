// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { useTranslation } from 'react-i18next';

interface ErrorBannerProps {
  message: string;
  onDismiss?: () => void;
  /** Optional dev-only debugging payload — typically the truncated raw
   *  response from a `WireShapeError`. When set, a "raw response"
   *  disclosure renders below the message in dev builds only. Hidden
   *  entirely in production. */
  details?: string;
  /** Visible heading on the disclosure (defaults to "raw response"). */
  detailsLabel?: string;
}

// Vite injects `import.meta.env.DEV` at build time. Read it once so prod
// builds tree-shake the disclosure block.
const IS_DEV = (import.meta as ImportMeta & { env?: { DEV?: boolean } }).env?.DEV === true;

export function ErrorBanner({ message, onDismiss, details, detailsLabel }: ErrorBannerProps) {
  const { t } = useTranslation('common');
  const [showDetails, setShowDetails] = useState(false);
  const showDisclosure = IS_DEV && !!details;

  return (
    <div className="bg-destructive/10 border border-destructive/30 rounded-md px-4 py-3 text-sm text-destructive mb-4">
      <div className="flex items-center justify-between">
        <span>{message}</span>
        <div className="flex items-center gap-3 ml-4">
          {showDisclosure && (
            <button
              type="button"
              onClick={() => setShowDetails((v) => !v)}
              className="text-destructive/70 hover:text-destructive underline text-xs transition-colors"
            >
              {showDetails ? t('hide') : (detailsLabel ?? t('rawResponse'))}
            </button>
          )}
          {onDismiss && (
            <button
              type="button"
              onClick={onDismiss}
              className="text-destructive/70 hover:text-destructive underline text-xs transition-colors"
            >
              {t('dismiss')}
            </button>
          )}
        </div>
      </div>
      {showDisclosure && showDetails && (
        <pre className="mt-2 text-xs text-destructive/80 bg-destructive/5 border border-destructive/20 rounded px-2 py-1 overflow-x-auto whitespace-pre-wrap break-all">
          {details}
        </pre>
      )}
    </div>
  );
}