import { create } from 'zustand';
import { useSettingsStore } from '../../stores/settingsStore';
import { amountText } from './ticketModel';

/**
 * What the ticket is set to: the typed amount and the chosen duration.
 *
 * Held outside the component because something other than the ticket needs to
 * set them. "Trade Again" on a result card - which lives in the app shell, not
 * on the Trade screen - puts the same amount and duration back and asks the
 * ticket to take focus. The amount is kept as the text in the field, not as a
 * number, so an empty or half-typed field is a state the ticket can name
 * rather than one it silently rewrites.
 */
interface TicketState {
  amountText: string;
  expirySeconds: number;
  /** Bumped to ask the ticket to scroll into view and focus the amount. */
  focusRequest: number;
  setAmountText: (text: string) => void;
  setExpiry: (seconds: number) => void;
  /** Pre-fills the ticket and asks it to take focus. */
  prefill: (stake: number, expirySeconds: number) => void;
}

const settings = useSettingsStore.getState();

export const useTicketStore = create<TicketState>((set) => ({
  amountText: amountText(settings.defaultStake),
  expirySeconds: settings.defaultExpirySeconds,
  focusRequest: 0,

  setAmountText: (text) => set({ amountText: text }),
  setExpiry: (expirySeconds) => {
    useSettingsStore.getState().setDefaultExpiry(expirySeconds);
    set({ expirySeconds });
  },
  prefill: (stake, expirySeconds) => {
    useSettingsStore.getState().setDefaultStake(stake);
    useSettingsStore.getState().setDefaultExpiry(expirySeconds);
    set((state) => ({ amountText: amountText(stake), expirySeconds, focusRequest: state.focusRequest + 1 }));
  },
}));
