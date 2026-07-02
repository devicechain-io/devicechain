// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { Toaster, toast as sonnerToast } from 'sonner';

/**
 * Toast system, backed by `sonner` (the toast library shadcn/ui ships).
 *
 * We keep the original `ToastProvider` + `useToast()` surface so existing
 * call sites (`const { toast } = useToast(); toast(message, variant)`) keep
 * working unchanged — sonner handles stacking order, animation, swipe-to-
 * dismiss, and a11y, replacing our hand-rolled portal.
 */

type ToastVariant = 'success' | 'error' | 'info';

interface ToastAPI {
  toast: (message: string, variant?: ToastVariant) => void;
}

function show(message: string, variant: ToastVariant = 'success') {
  if (variant === 'success') return void sonnerToast.success(message);
  if (variant === 'error') return void sonnerToast.error(message);
  return void sonnerToast(message);
}

// sonner's toast() is a module-level singleton — no React context needed. We
// expose the same hook shape regardless so consumers don't change.
const api: ToastAPI = { toast: show };

export function useToast(): ToastAPI {
  return api;
}

/**
 * Mounts the sonner `<Toaster>` (top-right, dark theme to match the admin UI)
 * and renders children. Kept as a component named `ToastProvider` so the app
 * root mount point is unchanged.
 */
export function ToastProvider({ children }: { children: React.ReactNode }) {
  return (
    <>
      {children}
      <Toaster position="top-right" theme="dark" richColors closeButton />
    </>
  );
}