import { create } from 'zustand';
import { api } from '../services/api';
import { clearTokens, loadTokens, saveTokens } from '../services/http';
import type { User, Wallet } from '../types/api';

type AuthStatus = 'idle' | 'loading' | 'authenticated' | 'anonymous';

interface AuthState {
  status: AuthStatus;
  user: User | null;
  wallet: Wallet | null;
  error: string | null;

  hydrate: () => Promise<void>;
  login: (email: string, password: string) => Promise<void>;
  register: (input: { email: string; username: string; password: string; fullName?: string }) => Promise<void>;
  logout: () => Promise<void>;
  setUser: (user: User) => void;
}

export const useAuthStore = create<AuthState>((set) => ({
  status: 'idle',
  user: null,
  wallet: null,
  error: null,

  /** Restores a session from the stored refresh token on first paint. */
  async hydrate() {
    if (!loadTokens()) {
      set({ status: 'anonymous' });
      return;
    }
    set({ status: 'loading' });
    // A phone opens the app on a flaky connection far more often than a
    // browser opens a tab, and a sleeping server takes a while to answer its
    // first request. One retry covers both before giving up.
    for (let attempt = 0; attempt < 2; attempt += 1) {
      try {
        const { user, wallet } = await api.auth.me();
        set({ status: 'authenticated', user, wallet, error: null });
        return;
      } catch (err) {
        const status = (err as { status?: number }).status ?? 0;
        if (status === 401 || status === 403) {
          clearTokens();
          break;
        }
        // Unreachable is not refused: the stored session is kept, so the next
        // launch can still restore it instead of asking to sign in again.
        if (attempt === 0) await new Promise((resolve) => setTimeout(resolve, 1500));
      }
    }
    set({ status: 'anonymous', user: null, wallet: null });
  },

  async login(email, password) {
    set({ status: 'loading', error: null });
    try {
      const result = await api.auth.login(email, password);
      saveTokens(result.tokens);
      set({ status: 'authenticated', user: result.user, wallet: result.wallet });
    } catch (err) {
      set({ status: 'anonymous', error: (err as Error).message });
      throw err;
    }
  },

  async register(input) {
    set({ status: 'loading', error: null });
    try {
      const result = await api.auth.register(input);
      saveTokens(result.tokens);
      set({ status: 'authenticated', user: result.user, wallet: result.wallet });
    } catch (err) {
      set({ status: 'anonymous', error: (err as Error).message });
      throw err;
    }
  },

  async logout() {
    const tokens = loadTokens();
    try {
      await api.auth.logout(tokens?.refreshToken);
    } catch {
      /* the local session is cleared regardless of what the server says */
    }
    clearTokens();
    set({ status: 'anonymous', user: null, wallet: null });
  },

  setUser(user) {
    set({ user });
  },
}));
