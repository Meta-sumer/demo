import { create } from 'zustand';
import { api } from '../services/api';
import { clearTokens, loadTokens, saveTokens } from '../services/http';
import type { AdminUser } from '../types/api';

type AuthStatus = 'idle' | 'loading' | 'authenticated' | 'anonymous';

interface AuthState {
  status: AuthStatus;
  user: AdminUser | null;
  error: string | null;
  hydrate: () => Promise<void>;
  login: (email: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
  can: (permission: string) => boolean;
}

export const useAuthStore = create<AuthState>((set, get) => ({
  status: 'idle',
  user: null,
  error: null,

  async hydrate() {
    if (!loadTokens()) {
      set({ status: 'anonymous' });
      return;
    }
    set({ status: 'loading' });
    try {
      const { user } = await api.auth.me();
      set({ status: 'authenticated', user });
    } catch {
      clearTokens();
      set({ status: 'anonymous', user: null });
    }
  },

  async login(email, password) {
    set({ status: 'loading', error: null });
    try {
      const result = await api.auth.login(email, password);
      saveTokens(result.tokens);
      set({ status: 'authenticated', user: result.user });
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
      /* the local session is cleared regardless */
    }
    clearTokens();
    set({ status: 'anonymous', user: null });
  },

  /**
   * Menu-level convenience only. The server checks the same permission on every
   * request, so hiding a link here is never the actual boundary.
   */
  can(permission) {
    const user = get().user;
    if (!user) return false;
    if (user.role === 'SUPER_ADMIN') return true;
    return user.permissions.includes(permission);
  },
}));
