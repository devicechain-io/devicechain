// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';

import App from './App';
import { initAuth } from './auth';
import { ErrorBoundary } from './ErrorBoundary';
import './theme.css';

// Register the console's stored access token with the SDK before anything renders.
initAuth();

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ErrorBoundary>
      <App />
    </ErrorBoundary>
  </StrictMode>,
);
