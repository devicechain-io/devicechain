// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { GraphQLRequestError } from '@devicechain/client';
import { useBreakGlass } from './use-break-glass';

vi.mock('@/components/StepUpDialog', () => ({ StepUpDialog: ({ open, onSuccess, onOpenChange }: {
  open: boolean; onSuccess: () => void; onOpenChange: (open: boolean) => void;
}) => open && <div role="dialog" aria-label="Step up"><button onClick={() => { onOpenChange(false); onSuccess(); }}>Verify</button>
  <button onClick={() => onOpenChange(false)}>Cancel verification</button></div> }));
const refusal = (code: string) => new GraphQLRequestError(code, 200, [{ message: code, extensions: { code } }]);
afterEach(cleanup);
function show(fn: (reason?: string) => Promise<unknown>) {
  const done = vi.fn(); const failed = vi.fn();
  function Harness() {
    const flow = useBreakGlass();
    return <><button onClick={() => { void flow.run(fn).then(done, failed); }}>Run</button>{flow.element}</>;
  }
  render(<Harness />); fireEvent.click(screen.getByText('Run')); return { done, failed };
}
async function submitReason() {
  fireEvent.change(await screen.findByLabelText('Reason for the change'), { target: { value: 'Restore service' } });
  fireEvent.click(screen.getByRole('button', { name: 'Continue with this reason' }));
}
describe('break-glass flow', () => {
  it('returns the first successful result without asking for a reason', async () => {
    const fn = vi.fn().mockResolvedValue(7); const { done } = show(fn);
    await waitFor(() => expect(done).toHaveBeenCalledWith(7));
    expect(fn).toHaveBeenCalledExactlyOnceWith(); expect(screen.queryByRole('dialog')).toBeNull();
  });
  it('asks for a reason and retries with it', async () => {
    const fn = vi.fn().mockRejectedValueOnce(refusal('BREAK_GLASS_REASON_REQUIRED')).mockResolvedValue(8);
    const { done } = show(fn); await submitReason();
    await waitFor(() => expect(done).toHaveBeenCalledWith(8));
    expect(fn.mock.calls).toEqual([[], ['Restore service']]);
  });
  it.each(['STEP_UP_REQUIRED', 'SECOND_FACTOR_REQUIRED'])('keeps the reason through %s and retries once after verification', async code => {
    const fn = vi.fn().mockRejectedValueOnce(refusal('BREAK_GLASS_REASON_REQUIRED')).mockRejectedValueOnce(refusal(code)).mockResolvedValue(9);
    const { done } = show(fn); await submitReason();
    fireEvent.click(await screen.findByText('Verify'));
    await waitFor(() => expect(done).toHaveBeenCalledWith(9));
    expect(fn.mock.calls).toEqual([[], ['Restore service'], ['Restore service']]);
  });
  it('cancels without retrying', async () => {
    const fn = vi.fn().mockRejectedValueOnce(refusal('BREAK_GLASS_REASON_REQUIRED')); const { done } = show(fn);
    fireEvent.click(await screen.findByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(done).toHaveBeenCalledWith(undefined)); expect(fn).toHaveBeenCalledTimes(1);
  });
  it('cancels verification without a further retry', async () => {
    const fn = vi.fn().mockRejectedValueOnce(refusal('BREAK_GLASS_REASON_REQUIRED')).mockRejectedValueOnce(refusal('STEP_UP_REQUIRED'));
    const { done } = show(fn); await submitReason(); fireEvent.click(await screen.findByText('Cancel verification'));
    await waitFor(() => expect(done).toHaveBeenCalledWith(undefined)); expect(fn).toHaveBeenCalledTimes(2);
  });
  it('rethrows unrelated errors', async () => {
    const error = new Error('Offline'); const { failed } = show(vi.fn().mockRejectedValue(error));
    await waitFor(() => expect(failed).toHaveBeenCalledWith(error));
  });
});
