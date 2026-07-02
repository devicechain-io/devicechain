// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A top-level error boundary for the dashboard app: a widget or renderer that
// throws during render (a malformed definition, a bad option) takes down only the
// subtree, showing a themed fallback instead of a blank page. Class component
// because error boundaries have no hooks equivalent (getDerivedStateFromError /
// componentDidCatch are class-only).

import { Component, type ErrorInfo, type ReactNode } from 'react';

interface ErrorBoundaryProps {
  children: ReactNode;
}

interface ErrorBoundaryState {
  error: Error | null;
}

export class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  state: ErrorBoundaryState = { error: null };

  static getDerivedStateFromError(error: Error): ErrorBoundaryState {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo): void {
    // Surface it for diagnosis; the fallback keeps the app from going blank.
    console.error('Dashboard error boundary caught:', error, info);
  }

  render(): ReactNode {
    const { error } = this.state;
    if (!error) return this.props.children;
    return (
      <div
        style={{
          height: '100%',
          display: 'flex',
          flexDirection: 'column',
          alignItems: 'center',
          justifyContent: 'center',
          gap: 8,
          padding: 24,
          textAlign: 'center',
          color: 'hsl(var(--foreground))',
        }}
      >
        <div style={{ fontSize: 18, fontWeight: 600 }}>Something went wrong</div>
        <div style={{ color: 'hsl(var(--muted-foreground))', maxWidth: 420 }}>{error.message}</div>
      </div>
    );
  }
}
