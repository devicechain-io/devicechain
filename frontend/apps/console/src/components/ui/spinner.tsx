// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { cn } from '@/lib/utils';

interface SpinnerProps {
  size?: 'sm' | 'md';
  className?: string;
}

export function Spinner({ size = 'sm', className }: SpinnerProps) {
  return (
    <div
      className={cn(
        'border-2 border-primary border-t-transparent rounded-full animate-spin',
        size === 'sm' ? 'w-4 h-4' : 'w-6 h-6',
        className
      )}
    />
  );
}