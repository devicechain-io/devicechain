// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { Component, type ErrorInfo, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/button';

// The default fallback UI. Extracted into a function component so it can use the
// translation hook — the ErrorBoundary itself is a class (getDerivedStateFromError
// has no hook-based equivalent) and cannot call hooks.
function DefaultErrorFallback({ reset }: { reset: () => void }) {
  const { t } = useTranslation('common');
  return (
    <div className="flex h-full min-h-64 flex-col items-center justify-center gap-4 p-8 text-center">
      <div className="space-y-1">
        <p className="text-sm font-medium text-foreground">{t('errorBoundaryTitle')}</p>
        <p className="text-xs text-muted-foreground">{t('errorBoundaryBody')}</p>
      </div>
      <div className="flex gap-2">
        <Button variant="outline" size="sm" onClick={reset}>
          {t('tryAgain')}
        </Button>
        <Button size="sm" onClick={() => window.location.reload()}>
          {t('reload')}
        </Button>
      </div>
    </div>
  );
}

interface ErrorBoundaryProps {
  children: ReactNode;
  /** Optional custom fallback; receives the error and a reset callback. */
  fallback?: (error: Error, reset: () => void) => ReactNode;
}

interface ErrorBoundaryState {
  error: Error | null;
}

// A render-error net so a thrown component never blanks the app. Wrap routed
// content (keyed by location) so navigating away auto-recovers; the in-place
// "Try again" reset covers transient errors without a full reload.
export class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  state: ErrorBoundaryState = { error: null };

  static getDerivedStateFromError(error: Error): ErrorBoundaryState {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // Surface the stack in the console for diagnosis; the UI stays usable.
    console.error('Unhandled UI error:', error, info.componentStack);
  }

  reset = () => this.setState({ error: null });

  render() {
    const { error } = this.state;
    if (error) {
      if (this.props.fallback) return this.props.fallback(error, this.reset);
      return <DefaultErrorFallback reset={this.reset} />;
    }
    return this.props.children;
  }
}
