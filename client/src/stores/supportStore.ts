import { create } from 'zustand';
import type { SupportConversation, SupportMessage, SupportThread } from '../types/api';

/**
 * The customer's support conversation, kept live by the websocket.
 *
 * Held in a store rather than a query because it is written from three
 * directions at once - the page's own sends, the server's echo of those sends,
 * and the desk's replies - and messages must merge by id so that the echo of
 * a message already on screen never shows it twice.
 */

/** A message the customer has sent that the server has not confirmed yet. */
export interface PendingMessage {
  tempId: string;
  body: string;
  createdAt: string;
  failed: boolean;
}

interface SupportState {
  loaded: boolean;
  conversation: SupportConversation | null;
  messages: SupportMessage[];
  hasMore: boolean;
  pending: PendingMessage[];
  /** When the agent's "typing" signal lapses; null when nobody is typing. */
  agentTypingUntil: number | null;
  agentTypingName: string | null;

  setThread: (thread: SupportThread) => void;
  prependOlder: (thread: SupportThread) => void;
  addMessage: (message: SupportMessage) => void;
  setConversation: (conversation: SupportConversation) => void;
  addPending: (pending: PendingMessage) => void;
  resolvePending: (tempId: string, message?: SupportMessage) => void;
  failPending: (tempId: string) => void;
  setAgentTyping: (name: string | null) => void;
  reset: () => void;
}

/** A typing signal is repeated every few seconds while typing continues. */
const TYPING_TTL_MS = 5000;

function byTime(a: SupportMessage, b: SupportMessage): number {
  return a.createdAt === b.createdAt ? a.id.localeCompare(b.id) : a.createdAt.localeCompare(b.createdAt);
}

function merge(existing: SupportMessage[], incoming: SupportMessage[]): SupportMessage[] {
  const map = new Map(existing.map((m) => [m.id, m]));
  for (const m of incoming) map.set(m.id, m);
  return [...map.values()].sort(byTime);
}

const initial = {
  loaded: false,
  conversation: null,
  messages: [] as SupportMessage[],
  hasMore: false,
  pending: [] as PendingMessage[],
  agentTypingUntil: null,
  agentTypingName: null,
};

export const useSupportStore = create<SupportState>((set) => ({
  ...initial,

  setThread: (thread) =>
    set((state) => ({
      loaded: true,
      conversation: thread.conversation,
      messages: merge(state.messages, thread.items),
      hasMore: thread.hasMore,
    })),

  prependOlder: (thread) =>
    set((state) => ({ messages: merge(state.messages, thread.items), hasMore: thread.hasMore })),

  addMessage: (message) =>
    set((state) => ({
      messages: merge(state.messages, [message]),
      // A reply ends the "typing" indicator, whatever its timer says.
      ...(message.sender === 'AGENT' ? { agentTypingUntil: null } : {}),
    })),

  setConversation: (conversation) => set({ conversation }),

  addPending: (pending) => set((state) => ({ pending: [...state.pending, pending] })),

  resolvePending: (tempId, message) =>
    set((state) => ({
      pending: state.pending.filter((p) => p.tempId !== tempId),
      messages: message ? merge(state.messages, [message]) : state.messages,
    })),

  failPending: (tempId) =>
    set((state) => ({ pending: state.pending.map((p) => (p.tempId === tempId ? { ...p, failed: true } : p)) })),

  setAgentTyping: (name) =>
    set(name ? { agentTypingUntil: Date.now() + TYPING_TTL_MS, agentTypingName: name } : { agentTypingUntil: null }),

  reset: () => set(initial),
}));
