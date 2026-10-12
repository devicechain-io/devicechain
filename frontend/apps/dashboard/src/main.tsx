// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';

import App from './App';
import { ErrorBoundary } from './ErrorBoundary';
// Side-effect import: initializes the shared i18next instance (locale detection,
// catalogs, the <html lang> sync) before anything renders. It sits ABOVE the
// ErrorBoundary in this file's order on purpose — the boundary's own fallback is
// translated, so an error thrown during the first render must still find a
// configured i18next.
import './i18n/config';
import './theme.css';
import { SSO_COMPLETE_PATH, captureSsoReturn } from './sso';

// The single sign-on return carries a one-time code in its query. Take it out of the
// address bar (and stop the page sending a Referer) before anything renders.
if (window.location.pathname === SSO_COMPLETE_PATH) captureSsoReturn();

// Auth is interactive now — the App renders its own login form and registers the
// SDK token getter itself (this is the reference external viewer; it brings its own
// session rather than reusing the console's).
createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ErrorBoundary>
      <App />
    </ErrorBoundary>
  </StrictMode>,
);
