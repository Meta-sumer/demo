import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import { persistStorage } from './storage';

/**
 * Whether this device has been through the five-step introduction.
 *
 * Per device rather than per account on purpose: it is a short explanation,
 * not a consent record, and someone signing in on a new phone loses nothing by
 * being offered it again - "Skip" is on every step. Replaying it from Learn
 * does not reset the flag; it is only read to decide whether to show it
 * unprompted after registration.
 */
interface OnboardingState {
  completed: boolean;
  complete: () => void;
}

export const useOnboardingStore = create<OnboardingState>()(
  persist(
    (set) => ({
      completed: false,
      complete: () => set({ completed: true }),
    }),
    { name: 'bt.onboarding', storage: persistStorage },
  ),
);
