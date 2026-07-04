// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';

import App from './App';
import { ErrorBoundary } from './ErrorBoundary';
import './theme.css';

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
