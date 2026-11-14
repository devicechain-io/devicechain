// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import '@/i18n/config';
import { useState } from 'react';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import { RevealOnceDialog } from './RevealOnceDialog';
import { Dialog, DialogContent, DialogTitle } from './ui/dialog';

afterEach(cleanup);

function Reveal() {
  const [value, setValue] = useState<string | null>('one-time-value');
  return value && <RevealOnceDialog title="Issued secret" onDone={() => setValue(null)} doneLabel="Done">
    <input aria-label="Secret" readOnly value={value} />
  </RevealOnceDialog>;
}

describe('reveal-once dialog', () => {
  it('survives Escape and outside clicks, has no close icon, and clears on acknowledgement', async () => {
    render(<Reveal />);
    const dialog = screen.getByRole('dialog', { name: 'Issued secret' });
    await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true));
    fireEvent.keyDown(document.activeElement!, { key: 'Escape' });
    const overlay = document.querySelector('[data-slot="dialog-overlay"]')!;
    fireEvent.pointerDown(overlay, { button: 0, pointerType: 'mouse' });
    fireEvent.click(overlay);
    expect(screen.getByLabelText('Secret')).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Close' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Done' }));
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(screen.queryByDisplayValue('one-time-value')).toBeNull();
  });

  it('allows ordinary dialogs to dismiss with Escape', async () => {
    function Ordinary() {
      const [open, setOpen] = useState(true);
      return <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent aria-describedby={undefined}><DialogTitle>Ordinary</DialogTitle></DialogContent>
      </Dialog>;
    }
    render(<Ordinary />);
    const close = screen.getByRole('button', { name: 'Close' });
    await waitFor(() => expect(document.activeElement).toBe(close));
    fireEvent.keyDown(close, { key: 'Escape' });
    expect(screen.queryByRole('dialog')).toBeNull();
  });
});
