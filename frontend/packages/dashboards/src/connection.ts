// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The hub's live-connection state, as a viewer should read it: is the data on this board
// arriving (live), is the socket down and being retried (reconnecting), or has it been down
// long enough, or the browser itself gone offline, that the board should be treated as frozen
// (offline).
//
// 🔴 It is derived from the SOCKET events the client already surfaces (connected / closed),
// per open subscription, not from the age of any sample. A quiet sensor on a healthy socket
// is `live`; sample age is the freshness indicator's job, and the two answer different
// questions.

export type HubConnectionState = 'live' | 'reconnecting' | 'offline';

// How long a socket may stay down before "reconnecting" is no longer an honest word for it.
// graphql-ws retries with a growing back-off, so a drop that outlasts this is not a blip.
export const OFFLINE_AFTER_MS = 20_000;

// One open subscription's view of its socket. A subscription starts UP: the shared per-area
// socket may already be connected, and the client then emits no `connected` for a late
// joiner, so waiting for one would leave a healthy board stuck "reconnecting".
export interface ConnectionHandle {
  connected(): void;
  closed(): void;
  // The subscription is gone; its socket state no longer counts.
  release(): void;
}

export class ConnectionTracker {
  private readonly down = new Set<ConnectionHandle>();
  private readonly listeners = new Set<() => void>();
  private escalated = false;
  private escalate: ReturnType<typeof setTimeout> | undefined;
  private browserOffline = false;
  private current: HubConnectionState = 'live';
  private detach: (() => void) | undefined;

  constructor(private readonly offlineAfterMs: number = OFFLINE_AFTER_MS) {}

  get state(): HubConnectionState {
    return this.current;
  }

  register(): ConnectionHandle {
    const handle: ConnectionHandle = {
      connected: () => {
        this.down.delete(handle);
        this.settle();
      },
      closed: () => {
        this.down.add(handle);
        this.settle();
      },
      release: () => {
        this.down.delete(handle);
        this.settle();
      },
    };
    return handle;
  }

  // Listeners are what keep the browser online/offline hooks attached, so a hub nobody is
  // watching holds no window listeners. Returns the unsubscribe.
  subscribe(listener: () => void): () => void {
    this.listeners.add(listener);
    if (this.listeners.size === 1) this.attach();
    return () => {
      this.listeners.delete(listener);
      if (this.listeners.size === 0) this.release();
    };
  }

  private attach(): void {
    if (typeof window === 'undefined') return;
    const sync = (): void => {
      this.browserOffline = typeof navigator !== 'undefined' && navigator.onLine === false;
      this.settle();
    };
    window.addEventListener('offline', sync);
    window.addEventListener('online', sync);
    this.detach = () => {
      window.removeEventListener('offline', sync);
      window.removeEventListener('online', sync);
    };
    sync();
  }

  private release(): void {
    this.detach?.();
    this.detach = undefined;
    this.browserOffline = false;
    this.settle();
  }

  private settle(): void {
    if (this.down.size === 0) {
      if (this.escalate) clearTimeout(this.escalate);
      this.escalate = undefined;
      this.escalated = false;
    } else if (!this.escalate && !this.escalated) {
      this.escalate = setTimeout(() => {
        this.escalate = undefined;
        this.escalated = true;
        this.settle();
      }, this.offlineAfterMs);
    }
    const next: HubConnectionState = this.browserOffline
      ? 'offline'
      : this.down.size === 0
        ? 'live'
        : this.escalated
          ? 'offline'
          : 'reconnecting';
    if (next === this.current) return;
    this.current = next;
    for (const listener of [...this.listeners]) listener();
  }
}
