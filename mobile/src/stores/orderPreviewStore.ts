import { create } from 'zustand';
import type { Direction } from '../types/api';

/**
 * The order the trader is about to place.
 *
 * This is the only piece of order state the client owns outright. Everything
 * else on the chart mirrors a server row; a preview has no row yet, and must
 * never be mistaken for one - it holds no trade id, and it disappears the
 * moment the server answers.
 *
 * Two phases matter:
 *
 *   INTENT   the pointer is on UP or DOWN but nothing has been sent. The
 *            window slides with the clock and the line follows the market,
 *            because the trader has not committed to a price or a moment.
 *   PLACING  the request is in flight. Price and time are frozen at the instant
 *            of the click, so the preview sits exactly where the order was
 *            asked for, even as the market keeps moving underneath it.
 *
 * REJECTED is the short tail of PLACING: the server refused, and the trader is
 * owed a visible cancellation rather than a marker that silently vanishes.
 */
export type PreviewPhase = 'INTENT' | 'PLACING' | 'REJECTED';

export interface OrderPreview {
  id: string;
  assetId: string;
  direction: Direction;
  stake: number;
  currency: string;
  expirySeconds: number;
  payoutPercent: number;
  potentialReturn: number;
  /** null while the line tracks the live market price. */
  entryPrice: number | null;
  /** null while the window slides with the clock. */
  entryTimestamp: number | null;
  expiryTimestamp: number | null;
  phase: PreviewPhase;
  /** When the current phase began; drives the cancelled fade. */
  phaseAt: number;
}

/** What a caller supplies; the store fills in identity and timing. */
export interface PreviewDraft {
  assetId: string;
  direction: Direction;
  stake: number;
  currency: string;
  expirySeconds: number;
  payoutPercent: number;
  potentialReturn: number;
}

interface OrderPreviewState {
  preview: OrderPreview | null;
  /** Pointer is over a direction button: show an untethered preview. */
  showIntent: (draft: PreviewDraft) => void;
  /** Pointer left the button. Only clears an intent, never a live request. */
  clearIntent: (assetId?: string) => void;
  /** The order was sent. Freezes it at `entryPrice` and the current instant. */
  commit: (draft: PreviewDraft, entryPrice: number | null) => void;
  /** The server refused. Shows the cancelled state, then clears itself. */
  reject: () => void;
  /** The server accepted; the real trade takes over the drawing. */
  resolve: () => void;
  clear: () => void;
}

/**
 * The cancelled state clears itself on a timer. Kept at module scope so a
 * second rejection cannot leave the first one's timer running and wipe a
 * preview that has since been replaced.
 */
let clearTimer: ReturnType<typeof setTimeout> | null = null;

function cancelTimer() {
  if (clearTimer !== null) {
    clearTimeout(clearTimer);
    clearTimer = null;
  }
}

export const useOrderPreviewStore = create<OrderPreviewState>((set, get) => ({
  preview: null,

  showIntent(draft) {
    const current = get().preview;
    // A request in flight outranks a hover. Without this, moving the pointer
    // back over the button mid-request would unfreeze the entry price.
    if (current && current.phase !== 'INTENT') return;
    if (
      current &&
      current.assetId === draft.assetId &&
      current.direction === draft.direction &&
      current.stake === draft.stake &&
      current.expirySeconds === draft.expirySeconds
    ) {
      return;
    }

    cancelTimer();
    set({
      preview: {
        ...draft,
        id: 'preview',
        entryPrice: null,
        entryTimestamp: null,
        expiryTimestamp: null,
        phase: 'INTENT',
        phaseAt: Date.now(),
      },
    });
  },

  clearIntent(assetId) {
    const current = get().preview;
    if (!current || current.phase !== 'INTENT') return;
    if (assetId && current.assetId !== assetId) return;
    set({ preview: null });
  },

  commit(draft, entryPrice) {
    cancelTimer();
    const now = Date.now();
    set({
      preview: {
        ...draft,
        id: 'preview',
        entryPrice,
        entryTimestamp: now,
        expiryTimestamp: now + draft.expirySeconds * 1000,
        phase: 'PLACING',
        phaseAt: now,
      },
    });
  },

  reject() {
    const current = get().preview;
    if (!current) return;
    cancelTimer();
    set({ preview: { ...current, phase: 'REJECTED', phaseAt: Date.now() } });
    clearTimer = setTimeout(() => {
      clearTimer = null;
      // Only clear what is still the rejected preview - a new order may have
      // been placed in the meantime.
      const latest = useOrderPreviewStore.getState().preview;
      if (latest?.phase === 'REJECTED') set({ preview: null });
    }, 1800);
  },

  resolve() {
    cancelTimer();
    set({ preview: null });
  },

  clear() {
    cancelTimer();
    set({ preview: null });
  },
}));
