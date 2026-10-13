import { useEffect, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { Modal } from '../../components/ui';
import { useWorkspaceStore } from '../../stores/workspaceStore';
import { DepositForm, WithdrawForm } from './Transfer';

/**
 * Deposit and withdrawal, over whatever is on screen.
 *
 * These were full screens, which meant funding an account cost you sight of
 * the chart and any position on it. The forms are unchanged - the same claim,
 * the same manual review - only the frame around them is.
 *
 * Mounted once by the signed-in shell; opened with `openFunds(kind)`.
 */
export function TransferModal() {
  const kind = useWorkspaceStore((s) => s.transferModal);
  const close = useWorkspaceStore((s) => s.closeTransferModal);
  const queryClient = useQueryClient();

  // The sheet slides away after `kind` clears; keep its title and form on it
  // until it has gone rather than flipping to the other flow on the way out.
  const [shown, setShown] = useState(kind);
  useEffect(() => {
    if (kind) setShown(kind);
  }, [kind]);

  const isDeposit = (kind ?? shown) === 'DEPOSIT';

  function onDone() {
    // The balance in the top bar is on screen behind the sheet, so it has to
    // be right the moment this closes.
    // `['wallet']` also covers both account balances and the ledger.
    void queryClient.invalidateQueries({ queryKey: ['wallet'] });
    close();
  }

  return (
    <Modal
      open={kind !== null}
      onClose={close}
      title={isDeposit ? 'Deposit' : 'Withdraw'}
      subtitle={
        isDeposit
          ? 'Pay to the account shown, then submit the reference so an operator can verify it.'
          : 'The amount is reserved immediately and paid out once an operator approves it.'
      }
    >
      {kind === 'DEPOSIT' || (!kind && shown === 'DEPOSIT') ? <DepositForm onDone={onDone} /> : null}
      {kind === 'WITHDRAWAL' || (!kind && shown === 'WITHDRAWAL') ? <WithdrawForm onDone={onDone} /> : null}
    </Modal>
  );
}
