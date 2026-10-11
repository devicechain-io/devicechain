// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Per-widget failure containment. Every widget implementation renders inside its own error
// boundary: a component that throws shows a local diagnostic with a reset control while its
// siblings keep rendering, and a host callback that rejects (an action, a candidate lookup)
// is surfaced on the widget that made the call instead of vanishing into an unhandled
// rejection. An error boundary cannot contain an infinite loop or hostile same-page code;
// trusted package installation remains the operator's responsibility.

import { Component, createContext, useCallback, useContext, useState, type ReactNode } from 'react';

import { WidgetFrame } from './frame';
import { describeError, type ReportRejection } from './widget-host';

const MESSAGE_STYLE = {
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'center',
  justifyContent: 'center',
  gap: 4,
  width: '100%',
  height: '100%',
  padding: 12,
  boxSizing: 'border-box',
  textAlign: 'center',
  color: 'hsl(var(--muted-foreground))',
} as const;

const BUTTON_STYLE = {
  marginTop: 4,
  fontSize: 11,
  padding: '2px 8px',
  cursor: 'pointer',
} as const;

// The reporter a body uses for async host callbacks. A no-op outside a containment, so a
// component rendered on its own never needs one.
const ReportContext = createContext<ReportRejection>(() => {});

export function useReportRejection(): ReportRejection {
  return useContext(ReportContext);
}

interface BoundaryProps {
  title?: string;
  // Changes when the widget's stored configuration changes, so an edit retries a widget
  // that threw on the old one.
  resetKey: string;
  onReset: () => void;
  children: ReactNode;
}

interface BoundaryState {
  error: { message: string } | null;
  resetKey: string;
}

class WidgetErrorBoundary extends Component<BoundaryProps, BoundaryState> {
  state: BoundaryState = { error: null, resetKey: this.props.resetKey };

  static getDerivedStateFromError(error: unknown): Partial<BoundaryState> {
    return { error: { message: describeError(error) } };
  }

  static getDerivedStateFromProps(props: BoundaryProps, state: BoundaryState): Partial<BoundaryState> | null {
    if (props.resetKey === state.resetKey) return null;
    return { error: null, resetKey: props.resetKey };
  }

  private reset = (): void => {
    this.props.onReset();
    this.setState({ error: null });
  };

  render(): ReactNode {
    const { error } = this.state;
    if (!error) return this.props.children;
    return (
      <WidgetFrame title={this.props.title}>
        <div style={MESSAGE_STYLE} role="alert">
          <div style={{ fontSize: 20, lineHeight: 1 }} aria-hidden="true">
            ⚠
          </div>
          <div style={{ fontSize: 13, fontWeight: 600 }}>This widget failed to render</div>
          <div style={{ fontSize: 11, opacity: 0.8, wordBreak: 'break-word' }}>{error.message}</div>
          <button type="button" style={BUTTON_STYLE} onClick={this.reset}>
            Reset widget
          </button>
        </div>
      </WidgetFrame>
    );
  }
}

export interface WidgetContainmentProps {
  title?: string;
  resetKey: string;
  children: ReactNode;
}

// WidgetContainment wraps ONE widget. Rejections reported by the host callbacks it hands
// down appear in a dismissible strip over the widget's bottom edge.
export function WidgetContainment({ title, resetKey, children }: WidgetContainmentProps) {
  const [messages, setMessages] = useState<string[]>([]);
  const report = useCallback<ReportRejection>((message) => {
    setMessages((prev) => (prev.includes(message) ? prev : [...prev, message].slice(-3)));
  }, []);
  const clear = useCallback(() => setMessages([]), []);
  return (
    <div style={{ position: 'relative', width: '100%', height: '100%' }}>
      <ReportContext.Provider value={report}>
        <WidgetErrorBoundary title={title} resetKey={resetKey} onReset={clear}>
          {children}
        </WidgetErrorBoundary>
      </ReportContext.Provider>
      {messages.length > 0 && (
        <div
          role="status"
          style={{
            position: 'absolute',
            left: 4,
            right: 4,
            bottom: 4,
            fontSize: 11,
            padding: '2px 6px',
            borderRadius: 4,
            background: 'hsl(var(--destructive, 0 70% 45%))',
            color: 'hsl(var(--destructive-foreground, 0 0% 100%))',
            display: 'flex',
            gap: 6,
            alignItems: 'center',
          }}
        >
          <span style={{ flex: 1, wordBreak: 'break-word' }}>{messages.join(' · ')}</span>
          <button type="button" aria-label="Dismiss" onClick={clear} style={{ cursor: 'pointer' }}>
            ×
          </button>
        </div>
      )}
    </div>
  );
}
