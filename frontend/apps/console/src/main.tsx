// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter } from 'react-router-dom';
import { ThemeProvider } from '@/components/ThemeProvider';
import { ToastProvider } from '@/components/ui/toast';
import { ConfirmProvider } from '@/components/ui/confirm-dialog';
import { enableSchemaNegotiation } from '@devicechain/client';
import { schemaNegotiationWanted } from '@/lib/schema-negotiation';
import { AuthProvider } from '@/auth/AuthProvider';
import { AreaCapabilityProvider } from '@/lib/capabilities';
import { SSO_COMPLETE_PATH, captureSsoReturn } from '@/lib/sso';
import { installChunkReload } from '@/lib/chunkReload';
import App from './App';
// Initializes the shared i18next instance (ADR-066) as a side effect, before any
// component renders — react-i18next reads it through context/useTranslation.
import i18n, { i18nReady } from '@/i18n/config';
import { Spinner } from '@/components/ui/spinner';
import '@fontsource-variable/inter';
import './index.css';

// The single sign-on return carries a one-time code in its query. Take it out of the
// address bar (and stop the page sending a Referer) before the router reads the location
// or anything renders; the completion page picks the captured code up from lib/sso.
if (window.location.pathname === SSO_COMPLETE_PATH) captureSsoReturn();

// A page chunk named by a previous deploy's index.html is gone after an upgrade: reload once.
installChunkReload(window);

// Check each authenticated request against the schema the connected instance serves, so a console
// pointed at an older instance refuses what it cannot serve instead of sending it. Only where the
// console and the instance can be different releases: `vite dev`, or a build made with
// VITE_SCHEMA_NEGOTIATION=on for a console hosted apart from its instance. A production console
// served by its own instance is that instance's release, so it stays off (see lib/schema-negotiation).
if (schemaNegotiationWanted(import.meta.env)) enableSchemaNegotiation();

const root = createRoot(document.getElementById('root')!);

// English is bundled, so its first paint needs nothing. Another language's catalogs are fetched
// on demand: hold the real app back until they are in, so a Spanish operator never sees English
// flash first. i18nReady never rejects (a failed load falls back to English). The placeholder is a
// bare spinner, not LoadingState: it shows before any catalog is in, so its "Loading..." would be
// English on exactly the screens this wait exists to keep English off.
if (!i18n.hasLoadedNamespace('common')) {
  root.render(
    <div className="flex h-screen items-center justify-center" aria-busy="true" data-testid="i18n-loading">
      <Spinner size="md" />
    </div>,
  );
}
void i18nReady.then(() => root.render(
  <StrictMode>
    <ThemeProvider defaultTheme="dark">
      <ToastProvider>
        <AuthProvider>
          <ConfirmProvider>
            <BrowserRouter>
              {/* Inside the router because it retries on navigation, and inside
                  AuthProvider because it asks over the caller's own session. */}
              <AreaCapabilityProvider>
                <App />
              </AreaCapabilityProvider>
            </BrowserRouter>
          </ConfirmProvider>
        </AuthProvider>
      </ToastProvider>
    </ThemeProvider>
  </StrictMode>,
));
