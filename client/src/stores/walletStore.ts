import { create } from 'zustand';
import type { Wallet } from '../types/api';

interface WalletState {
  wallet: Wallet | null;
  /** Set when a websocket update arrives, so the header can flash the change. */
  lastChangeAt: number;
  setWallet: (wallet: Wallet) => void;
  reset: () => void;
}

export const useWalletStore = create<WalletState>((set) => ({
  wallet: null,
  lastChangeAt: 0,
  setWallet: (wallet) => set({ wallet, lastChangeAt: Date.now() }),
  reset: () => set({ wallet: null, lastChangeAt: 0 }),
}));
