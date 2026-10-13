import { useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '../../services/api';
import { ApiRequestError } from '../../services/http';
import { useAccountStore } from '../../stores/accountStore';
import { useWorkspaceStore } from '../../stores/workspaceStore';
import { toast } from '../../hooks/useToast';

/**
 * Opens the deposit or withdrawal flow.
 *
 * Money only ever moves in and out of the LIVE account - demo balance is
 * virtual and cannot be paid out or topped up with real money. Rather than
 * refusing on demo, the switch to live happens first and is said out loud, so
 * nobody finishes a deposit believing it went to the practice balance.
 */
export function openFunds(kind: 'DEPOSIT' | 'WITHDRAWAL'): void {
  const accounts = useAccountStore.getState();
  if (accounts.active !== 'LIVE') {
    accounts.setActive('LIVE');
    toast('info', 'Switched to your Live account', 'Deposits and withdrawals use real money, so they are Live only.');
  }
  useWorkspaceStore.getState().openTransferModal(kind);
}

/**
 * Tops the demo balance back up to its starting amount.
 *
 * The server refuses while demo trades are still open (409) and says why;
 * that message is shown as it is, because it tells the customer exactly what
 * to wait for.
 */
export function useResetDemo() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: api.wallet.resetDemo,
    onSuccess: (wallet) => {
      useAccountStore.getState().upsertWallet({ ...wallet, accountType: wallet.accountType ?? 'DEMO' });
      void queryClient.invalidateQueries({ queryKey: ['wallet'] });
      toast('success', 'Demo balance reset');
    },
    onError: (err) =>
      toast(
        'error',
        'Could not reset the demo balance',
        err instanceof ApiRequestError ? err.message : 'Please try again in a moment.',
      ),
  });
}
