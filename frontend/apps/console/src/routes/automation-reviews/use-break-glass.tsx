// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { lazy, Suspense, useEffect, useRef, useState } from 'react';
import { serverErrorCode } from '@/i18n/signInError';

const BreakGlass = lazy(() => import('./break-glass'));
export interface BreakGlassRequest {
  retry: (reason: string) => Promise<unknown>;
  resolve: (value: unknown) => void;
  reject: (error: unknown) => void;
}
export function useBreakGlass() {
  const [request, setRequest] = useState<BreakGlassRequest | null>(null);
  const pending = useRef<BreakGlassRequest | null>(null);
  useEffect(() => () => { pending.current?.resolve(undefined); }, []);
  const finish = () => { pending.current = null; setRequest(null); };
  const run = async <T,>(fn: (reason?: string) => Promise<T>): Promise<T | undefined> => {
    try { return await fn(); }
    catch (err) {
      if (serverErrorCode(err) !== 'BREAK_GLASS_REASON_REQUIRED') throw err;
      return new Promise<T | undefined>((resolve, reject) => {
        pending.current?.resolve(undefined);
        const next: BreakGlassRequest = {
          retry: fn,
          resolve: value => { finish(); resolve(value as T | undefined); },
          reject: error => { finish(); reject(error); },
        };
        pending.current = next; setRequest(next);
      });
    }
  };
  return { run, element: request && <Suspense fallback={null}><BreakGlass request={request} /></Suspense> };
}
