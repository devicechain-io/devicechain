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
import { configureSchemaNegotiation } from './schema-negotiation';
import { SSO_COMPLETE_PATH, captureSsoReturn } from './sso';

// The single sign-on return carries a one-time code in its query. Take it out of the
// address bar (and stop the page sending a Referer) before anything renders.
if (window.location.pathname === SSO_COMPLETE_PATH) captureSsoReturn();

// Check each authenticated request against the schema the connected instance serves, so a viewer
// pointed at an older instance refuses what it cannot serve instead of sending it. Only where the
// viewer and the instance can be different releases: `vite dev`, or a build made with
// VITE_SCHEMA_NEGOTIATION=on for a viewer hosted apart from its instance. A viewer served by its
// own instance is that instance's release, so it stays off (see ./schema-negotiation).
configureSchemaNegotiation(import.meta.env);

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
