import { useQueryClient } from '@tanstack/react-query';
import { Modal } from '../../components/Modal';
import { useWorkspaceStore } from '../../stores/workspaceStore';
import { DepositForm, WithdrawForm } from '../../pages/Transfer';

/**
 * Deposit and withdrawal, over the workspace.
 *
 * These were full pages, which meant funding an account cost you sight of the
 * chart and any position on it. The forms are unchanged - the same claim, the
 * same manual review - only the frame around them is.
 */
export function TransferModal() {
  const kind = useWorkspaceStore((s) => s.transferModal);
  const close = useWorkspaceStore((s) => s.closeTransferModal);
  const queryClient = useQueryClient();

  const isDeposit = kind === 'DEPOSIT';

  function onDone() {
    // The wallet header is on screen behind the modal, so it has to be right
    // the moment this closes.
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
      width={isDeposit ? 'sm:w-[min(1060px,94vw)]' : 'sm:w-[min(560px,94vw)]'}
    >
      {kind === 'DEPOSIT' ? <DepositForm onDone={onDone} /> : null}
      {kind === 'WITHDRAWAL' ? <WithdrawForm onDone={onDone} /> : null}
    </Modal>
  );
}
