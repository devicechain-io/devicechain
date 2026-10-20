// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter } from 'react-router-dom';
import { ThemeProvider } from '@/components/ThemeProvider';
import { ToastProvider } from '@/components/ui/toast';
import { ConfirmProvider } from '@/components/ui/confirm-dialog';
import { AuthProvider } from '@/auth/AuthProvider';
import { AreaCapabilityProvider } from '@/lib/capabilities';
import { SSO_COMPLETE_PATH, captureSsoReturn } from '@/lib/sso';
import { installChunkReload } from '@/lib/chunkReload';
import App from './App';
// Initializes the shared i18next instance (ADR-066) as a side effect, before any
// component renders — react-i18next reads it through context/useTranslation.
import i18n, { i18nReady } from '@/i18n/config';
import { LoadingState } from '@/components/ui/loading-state';
import '@fontsource-variable/inter';
import './index.css';

// The single sign-on return carries a one-time code in its query. Take it out of the
// address bar (and stop the page sending a Referer) before the router reads the location
// or anything renders; the completion page picks the captured code up from lib/sso.
if (window.location.pathname === SSO_COMPLETE_PATH) captureSsoReturn();

// A page chunk named by a previous deploy's index.html is gone after an upgrade: reload once.
installChunkReload(window);

const root = createRoot(document.getElementById('root')!);

// English is bundled, so its first paint needs nothing. Another language's catalogs are fetched
// on demand: hold the real app back until they are in, so a Spanish operator never sees English
// flash first. i18nReady never rejects (a failed load falls back to English).
if (!i18n.hasLoadedNamespace('common')) root.render(<LoadingState />);
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
