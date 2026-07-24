// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A themed confirm-before-acting dialog, exposed through an imperative
// `useConfirm()` hook so call sites read almost exactly like the native
// `window.confirm` they replace:
//
//   const confirm = useConfirm();
//   if (!(await confirm({ title: 'Delete device', description: '…' }))) return;
//   await remove();
//
// One <ConfirmProvider> near the app root renders a single centered dialog and
// resolves the pending promise with the user's choice. Built on the same
// @radix-ui/react-dialog primitive as the Sheet so the scrim, animation, focus
// trap, and Esc/outside-click handling match the rest of the console.

import * as React from 'react';
import { useTranslation } from 'react-i18next';
import * as DialogPrimitive from '@radix-ui/react-dialog';

import { Button } from '@/components/ui/button';
import { cn } from '@/lib/utils';

export interface ConfirmOptions {
  title: string;
  description?: React.ReactNode;
  /** Confirm button label. Defaults to 'Delete'. */
  confirmLabel?: string;
  /** Cancel button label. Defaults to 'Cancel'. */
  cancelLabel?: string;
  /** When true (default) the confirm button uses the destructive variant. */
  destructive?: boolean;
}

type ConfirmFn = (options: ConfirmOptions) => Promise<boolean>;

const ConfirmContext = React.createContext<ConfirmFn | null>(null);

// useConfirm returns an async confirm(options) that resolves true if the user
// confirms and false if they cancel/dismiss — a drop-in for window.confirm.
export function useConfirm(): ConfirmFn {
  const ctx = React.useContext(ConfirmContext);
  if (!ctx) throw new Error('useConfirm must be used within a <ConfirmProvider>');
  return ctx;
}

interface PendingRequest {
  options: ConfirmOptions;
  resolve: (confirmed: boolean) => void;
}

export function ConfirmProvider({ children }: { children: React.ReactNode }) {
  const [pending, setPending] = React.useState<PendingRequest | null>(null);
  // Mirror the pending request in a ref so confirm()/settle() resolve the promise
  // OUTSIDE a state updater (updaters must stay pure — a resolve inside one is
  // double-invoked under StrictMode) and without stale closures.
  const pendingRef = React.useRef<PendingRequest | null>(null);

  const confirm = React.useCallback<ConfirmFn>(
    (options) =>
      new Promise<boolean>((resolve) => {
        // A second confirm() while one is still open supersedes it — resolve the
        // superseded request false so its awaiter never hangs.
        pendingRef.current?.resolve(false);
        const request: PendingRequest = { options, resolve };
        pendingRef.current = request;
        setPending(request);
      }),
    [],
  );

  // settle resolves the outstanding promise once and clears the dialog.
  const settle = React.useCallback((confirmed: boolean) => {
    const request = pendingRef.current;
    if (!request) return;
    pendingRef.current = null;
    setPending(null);
    request.resolve(confirmed);
  }, []);

  const { t } = useTranslation('common');
  const options = pending?.options;
  // The confirm/cancel labels default to the shared `common` atoms so a caller
  // that omits them (most delete flows pass only their own destructive verb, or
  // nothing) gets a localized button rather than a hardcoded English one. A
  // caller may still override either with its own copy.
  const {
    title,
    description,
    confirmLabel = t('delete'),
    cancelLabel = t('cancel'),
    destructive = true,
  } = options ?? { title: '' };

  return (
    <ConfirmContext.Provider value={confirm}>
      {children}
      <DialogPrimitive.Root
        open={pending != null}
        onOpenChange={(open) => {
          // Any dismissal (Esc, outside click, close) counts as a cancel.
          if (!open) settle(false);
        }}
      >
        <DialogPrimitive.Portal>
          <DialogPrimitive.Overlay
            className={cn(
              'fixed inset-0 z-50 bg-black/80',
              'data-[state=open]:animate-in data-[state=closed]:animate-out',
              'data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0',
            )}
          />
          <DialogPrimitive.Content
            // With no description, opt out of Radix's aria-describedby (which
            // otherwise points at an empty description node) to avoid a dangling
            // reference + dev warning. When a description IS present, let Radix
            // wire it (don't pass the prop, or the linkage breaks).
            {...(description == null ? { 'aria-describedby': undefined } : {})}
            className={cn(
              'fixed left-1/2 top-1/2 z-50 grid w-full max-w-md -translate-x-1/2 -translate-y-1/2 gap-4',
              'rounded-lg border bg-background p-6 shadow-lg',
              'data-[state=open]:animate-in data-[state=closed]:animate-out',
              'data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0',
              'data-[state=closed]:zoom-out-95 data-[state=open]:zoom-in-95',
            )}
          >
            <div className="flex flex-col gap-2">
              <DialogPrimitive.Title className="text-lg font-semibold text-foreground">
                {title}
              </DialogPrimitive.Title>
              {description != null && (
                <DialogPrimitive.Description className="text-sm text-muted-foreground">
                  {description}
                </DialogPrimitive.Description>
              )}
            </div>
            <div className="flex justify-end gap-2">
              <Button variant="outline" onClick={() => settle(false)}>
                {cancelLabel}
              </Button>
              <Button
                variant={destructive ? 'destructive' : 'default'}
                onClick={() => settle(true)}
              >
                {confirmLabel}
              </Button>
            </div>
          </DialogPrimitive.Content>
        </DialogPrimitive.Portal>
      </DialogPrimitive.Root>
    </ConfirmContext.Provider>
  );
}
