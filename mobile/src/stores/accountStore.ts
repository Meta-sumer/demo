import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import { persistStorage } from './storage';
import type { AccountType, Wallet } from '../types/api';

/**
 * Which account the customer is trading on, and both balances.
 *
 * Demo and live share one interface (brief UX-16); only this context changes.
 * Every trade is placed on the active account, and the header, Home and the
 * ticket all read the balance from here, so a switch reaches all of them at
 * once. New customers start on demo: nobody should learn the product with
 * real money.
 */
interface AccountState {
  active: AccountType;
  wallets: Partial<Record<AccountType, Wallet>>;
  setActive: (account: AccountType) => void;
  setWallets: (wallets: Wallet[]) => void;
  upsertWallet: (wallet: Wallet) => void;
  reset: () => void;
}

export const useAccountStore = create<AccountState>()(
  persist(
    (set) => ({
      active: 'DEMO',
      wallets: {},
      setActive: (active) => set({ active }),
      setWallets: (wallets) =>
        set({ wallets: Object.fromEntries(wallets.map((w) => [w.accountType ?? 'LIVE', w])) }),
      upsertWallet: (wallet) =>
        set((state) => ({ wallets: { ...state.wallets, [wallet.accountType ?? 'LIVE']: wallet } })),
      reset: () => set({ wallets: {} }),
    }),
    { name: 'bt.account', storage: persistStorage, partialize: (state) => ({ active: state.active }) },
  ),
);

/** The wallet of the account being traded, or null until it has loaded. */
export function useActiveWallet(): Wallet | null {
  return useAccountStore((s) => s.wallets[s.active] ?? null);
}
