// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { ReactNode } from 'react';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog';

/** The owner holds the issued value in memory and clears it only on acknowledgement. */
export function RevealOnceDialog({ title, description, onDone, doneLabel, children, shot, contentClassName }: {
  title: string;
  description?: string;
  onDone: () => void;
  doneLabel: string;
  children: ReactNode;
  shot?: string;
  contentClassName?: string;
}) {
  return (
    <Dialog open>
      <DialogContent dismissible={false} data-shot={shot} className={contentClassName}
        {...(description == null ? { 'aria-describedby': undefined } : {})}>
        <div className="flex flex-col gap-2">
          <DialogTitle className="text-lg font-semibold text-foreground">{title}</DialogTitle>
          {description != null && <DialogDescription className="text-sm text-muted-foreground">{description}</DialogDescription>}
        </div>
        {children}
        <div className="flex justify-end"><Button onClick={onDone}>{doneLabel}</Button></div>
      </DialogContent>
    </Dialog>
  );
}
