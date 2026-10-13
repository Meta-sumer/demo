import { useCallback, useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent } from 'react';
import clsx from 'clsx';
import {
  ArrowDownToLine,
  ArrowUpFromLine,
  Check,
  CheckCheck,
  CircleAlert,
  Clock,
  Headset,
  LineChart,
  Lock,
  RotateCcw,
  SendHorizontal,
  ShieldCheck,
  UserRound,
} from 'lucide-react';
import { api } from '../services/api';
import { useAuthStore } from '../stores/authStore';
import { useSupportStore, type PendingMessage } from '../stores/supportStore';
import type { SupportMessage } from '../types/api';
import { Spinner } from '../components/ui';

/**
 * Live support chat.
 *
 * One continuous thread with the support desk. Messages appear the moment
 * either side sends them (websocket), the customer sees when the desk has read
 * what they wrote, and when an agent is typing. Sending is optimistic - the
 * bubble appears immediately and is confirmed, or offered for retry, when the
 * server answers.
 */

const MAX_LENGTH = 2000;
/** How often a "typing" signal may be repeated while the customer types. */
const TYPING_THROTTLE_MS = 3000;

const TOPICS: Array<{ icon: typeof LineChart; label: string; text: string }> = [
  { icon: ArrowDownToLine, label: 'Deposit', text: 'Hi, I have a question about a deposit.' },
  { icon: ArrowUpFromLine, label: 'Withdrawal', text: 'Hi, I need help with a withdrawal.' },
  { icon: LineChart, label: 'A trade', text: 'Hi, I have a question about one of my trades.' },
  { icon: UserRound, label: 'My account', text: 'Hi, I need help with my account.' },
];

export function SupportPage() {
  const user = useAuthStore((s) => s.user);
  const {
    loaded,
    conversation,
    messages,
    hasMore,
    pending,
    agentTypingUntil,
    agentTypingName,
    setThread,
    prependOlder,
    addPending,
    resolvePending,
    failPending,
    setConversation,
  } = useSupportStore();

  const [draft, setDraft] = useState('');
  const [loadError, setLoadError] = useState(false);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [now, setNow] = useState(() => Date.now());

  const scrollRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const stickToBottom = useRef(true);
  const lastTypingSent = useRef(0);

  /* ------------------------------------------------------------ loading */

  useEffect(() => {
    api.support
      .thread()
      .then(setThread)
      .catch(() => setLoadError(true));
  }, [setThread]);

  // The typing indicator lapses on its own; a clock tick lets it disappear.
  useEffect(() => {
    if (!agentTypingUntil) return;
    const timer = window.setInterval(() => setNow(Date.now()), 500);
    return () => window.clearInterval(timer);
  }, [agentTypingUntil]);
  const agentTyping = agentTypingUntil !== null && agentTypingUntil > now;

  /* --------------------------------------------------------- read state */

  // While the page is open and visible, anything the desk sends is read.
  const unread = conversation?.unreadByUser ?? 0;
  useEffect(() => {
    if (!loaded || unread === 0 || document.visibilityState !== 'visible') return;
    const timer = window.setTimeout(() => {
      api.support.markRead().then(setConversation).catch(() => undefined);
    }, 400);
    return () => window.clearTimeout(timer);
  }, [loaded, unread, setConversation]);

  useEffect(() => {
    const onVisible = () => {
      if (document.visibilityState === 'visible' && (useSupportStore.getState().conversation?.unreadByUser ?? 0) > 0) {
        api.support.markRead().then(setConversation).catch(() => undefined);
      }
    };
    document.addEventListener('visibilitychange', onVisible);
    return () => document.removeEventListener('visibilitychange', onVisible);
  }, [setConversation]);

  /* ---------------------------------------------------------- scrolling */

  const scrollToBottom = useCallback((smooth: boolean) => {
    const el = scrollRef.current;
    if (el) el.scrollTo({ top: el.scrollHeight, behavior: smooth ? 'smooth' : 'auto' });
  }, []);

  // New content keeps the view pinned to the bottom, unless the customer has
  // scrolled up to read history - then it is left alone.
  useLayoutEffect(() => {
    if (stickToBottom.current) scrollToBottom(loaded);
  }, [messages.length, pending.length, agentTyping, loaded, scrollToBottom]);

  function onScroll() {
    const el = scrollRef.current;
    if (!el) return;
    stickToBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
  }

  async function loadOlder() {
    const oldest = messages[0];
    if (!oldest || loadingOlder) return;
    const el = scrollRef.current;
    const heightBefore = el?.scrollHeight ?? 0;
    setLoadingOlder(true);
    try {
      prependOlder(await api.support.thread({ before: oldest.id }));
      // Keep the message the customer was looking at in the same place.
      requestAnimationFrame(() => {
        if (el) el.scrollTop += el.scrollHeight - heightBefore;
      });
    } finally {
      setLoadingOlder(false);
    }
  }

  /* ------------------------------------------------------------ sending */

  async function deliver(item: PendingMessage) {
    try {
      const result = await api.support.send(item.body);
      resolvePending(item.tempId, result.message);
      setConversation(result.conversation);
    } catch {
      failPending(item.tempId);
    }
  }

  function send(text = draft) {
    const body = text.trim();
    if (!body || body.length > MAX_LENGTH) return;
    const item: PendingMessage = {
      tempId: 'tmp-' + Date.now() + '-' + Math.random().toString(36).slice(2, 7),
      body,
      createdAt: new Date().toISOString(),
      failed: false,
    };
    addPending(item);
    setDraft('');
    stickToBottom.current = true;
    void deliver(item);
    inputRef.current?.focus();
  }

  function retry(item: PendingMessage) {
    resolvePending(item.tempId);
    const fresh = { ...item, tempId: item.tempId + 'r', failed: false };
    addPending(fresh);
    void deliver(fresh);
  }

  function onKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) {
      event.preventDefault();
      send();
    }
  }

  function onChange(value: string) {
    setDraft(value);
    const t = Date.now();
    if (value.trim() && t - lastTypingSent.current > TYPING_THROTTLE_MS) {
      lastTypingSent.current = t;
      api.support.typing().catch(() => undefined);
    }
  }

  // Grow the box with its content, up to a limit.
  useLayoutEffect(() => {
    const el = inputRef.current;
    if (!el) return;
    el.style.height = 'auto';
    el.style.height = Math.min(el.scrollHeight, 160) + 'px';
  }, [draft]);

  /* ------------------------------------------------------------- render */

  const resolved = conversation?.status === 'RESOLVED';
  const agentName = conversation?.assignedName ?? null;
  const empty = loaded && messages.length === 0 && pending.length === 0;

  return (
    <div className="mx-auto flex h-[calc(100dvh-3.5rem-3.5rem-2rem)] sm:h-[calc(100dvh-4rem-3.5rem-2rem)] lg:h-[calc(100dvh-4rem-2rem)] w-full max-w-4xl flex-col overflow-hidden rounded-2xl border border-surface-700 bg-surface-900 shadow-panel">
      {/* Header */}
      <header className="relative flex shrink-0 items-center gap-3 border-b border-surface-800 px-4 py-3.5 sm:px-5">
        <div
          aria-hidden
          className="pointer-events-none absolute inset-0 opacity-60"
          style={{ background: 'radial-gradient(420px 120px at 0% 0%, rgb(var(--accent) / 0.12), transparent 70%)' }}
        />
        <div className="relative grid h-11 w-11 shrink-0 place-items-center rounded-2xl bg-accent text-surface-950 shadow-raised">
          <Headset className="h-5 w-5" />
          <span className="absolute -bottom-0.5 -right-0.5 h-3 w-3 rounded-full border-2 border-surface-900 bg-up" />
        </div>
        <div className="relative min-w-0 flex-1">
          <h1 className="flex items-center gap-2 text-base font-bold text-content-100">
            Support
            <span className="rounded-full bg-accent/15 px-2 py-0.5 text-[10px] font-bold uppercase tracking-wider text-accent">
              Live chat
            </span>
          </h1>
          <p className="truncate text-xs text-content-400">
            {agentName ? (
              <>
                You are chatting with <span className="font-semibold text-content-200">{agentName}</span>
              </>
            ) : (
              'Write to us and a member of the support team will reply here'
            )}
          </p>
        </div>
        {conversation ? (
          <span
            className={clsx(
              'relative hidden rounded-full px-2.5 py-1 text-[11px] font-semibold sm:inline-flex',
              resolved ? 'bg-surface-800 text-content-300' : 'bg-up/15 text-up',
            )}
          >
            {resolved ? 'Resolved' : 'Open'}
          </span>
        ) : null}
      </header>

      {/* Thread */}
      <div ref={scrollRef} onScroll={onScroll} className="min-h-0 flex-1 overflow-y-auto px-3 py-4 sm:px-6">
        {!loaded && !loadError ? (
          <div className="grid h-full place-items-center">
            <Spinner className="text-accent" />
          </div>
        ) : loadError && !loaded ? (
          <div className="grid h-full place-items-center text-center">
            <div>
              <CircleAlert className="mx-auto h-6 w-6 text-down" />
              <p className="mt-2 text-sm text-content-300">Could not load the conversation.</p>
              <button
                type="button"
                onClick={() => window.location.reload()}
                className="mt-3 text-sm font-semibold text-accent hover:underline"
              >
                Try again
              </button>
            </div>
          </div>
        ) : empty ? (
          <Welcome name={user?.fullName ?? user?.username ?? null} onTopic={(text) => send(text)} />
        ) : (
          <>
            {hasMore ? (
              <div className="mb-4 text-center">
                <button
                  type="button"
                  onClick={loadOlder}
                  disabled={loadingOlder}
                  className="rounded-full border border-surface-700 bg-surface-850 px-3 py-1 text-xs font-medium text-content-300 transition hover:text-content-100"
                >
                  {loadingOlder ? 'Loading…' : 'Load earlier messages'}
                </button>
              </div>
            ) : null}

            <Thread
              messages={messages}
              pending={pending}
              agentReadAt={conversation?.agentReadAt ?? null}
              onRetry={retry}
            />

            {agentTyping ? <TypingBubble name={agentTypingName ?? 'Support'} /> : null}
          </>
        )}
      </div>

      {/* Composer */}
      <footer className="shrink-0 border-t border-surface-800 bg-surface-900 px-3 pb-3 pt-2.5 sm:px-5">
        {resolved ? (
          <p className="mb-2 text-center text-[11px] text-content-400">
            This conversation was resolved. Send a message to reopen it.
          </p>
        ) : null}
        <div className="flex items-end gap-2 rounded-2xl border border-surface-700 bg-surface-850 p-1.5 pl-3.5 transition focus-within:border-accent/60 focus-within:ring-2 focus-within:ring-accent/15">
          <textarea
            ref={inputRef}
            rows={1}
            value={draft}
            onChange={(event) => onChange(event.target.value)}
            onKeyDown={onKeyDown}
            placeholder="Type your message…"
            aria-label="Message"
            maxLength={MAX_LENGTH}
            disabled={!loaded}
            className="max-h-40 min-h-[40px] flex-1 resize-none bg-transparent py-2.5 text-sm text-content-100 outline-none focus-visible:outline-none placeholder:text-content-400"
          />
          <button
            type="button"
            onClick={() => send()}
            disabled={!draft.trim() || !loaded}
            aria-label="Send"
            className="grid h-10 w-10 shrink-0 place-items-center rounded-xl bg-accent text-surface-950 transition hover:brightness-110 disabled:cursor-not-allowed disabled:opacity-40"
          >
            <SendHorizontal className="h-[18px] w-[18px]" />
          </button>
        </div>
        <div className="mt-1.5 flex items-center justify-between px-1 text-[10px] text-content-400">
          <span className="flex items-center gap-1">
            <Lock className="h-3 w-3" />
            Enter to send · Shift + Enter for a new line
          </span>
          {draft.length > MAX_LENGTH - 200 ? (
            <span className={clsx('tnum', draft.length >= MAX_LENGTH && 'text-down')}>
              {draft.length} / {MAX_LENGTH}
            </span>
          ) : null}
        </div>
      </footer>
    </div>
  );
}

/* ================================================================ pieces */

function Welcome({ name, onTopic }: { name: string | null; onTopic: (text: string) => void }) {
  return (
    <div className="mx-auto flex h-full max-w-md flex-col items-center justify-center text-center">
      <div className="grid h-16 w-16 place-items-center rounded-3xl bg-accent/15 text-accent">
        <Headset className="h-8 w-8" />
      </div>
      <h2 className="mt-5 text-xl font-bold text-content-100">
        {name ? 'Hi ' + name.split(' ')[0] + ', how can we help?' : 'How can we help?'}
      </h2>
      <p className="mt-2 text-sm leading-relaxed text-content-400">
        Start a conversation with our support team. Replies arrive right here, and you can leave this page
        — we will keep the thread for you.
      </p>

      <div className="mt-6 grid w-full grid-cols-2 gap-2">
        {TOPICS.map((topic) => (
          <button
            key={topic.label}
            type="button"
            onClick={() => onTopic(topic.text)}
            className="flex items-center gap-2.5 rounded-xl border border-surface-700 bg-surface-850 px-3 py-3 text-left text-sm font-medium text-content-200 transition hover:border-accent/50 hover:text-content-100"
          >
            <topic.icon className="h-4 w-4 shrink-0 text-accent" />
            {topic.label}
          </button>
        ))}
      </div>

      <div className="mt-6 flex items-center gap-4 text-[11px] text-content-400">
        <span className="flex items-center gap-1">
          <ShieldCheck className="h-3.5 w-3.5 text-up" />
          Private to you and our team
        </span>
        <span className="flex items-center gap-1">
          <Clock className="h-3.5 w-3.5" />
          Conversation history is kept
        </span>
      </div>
      <p className="mt-4 text-[11px] text-content-400">We will never ask for your password in this chat.</p>
    </div>
  );
}

function dayLabel(iso: string): string {
  const date = new Date(iso);
  const today = new Date();
  const yesterday = new Date();
  yesterday.setDate(today.getDate() - 1);
  if (date.toDateString() === today.toDateString()) return 'Today';
  if (date.toDateString() === yesterday.toDateString()) return 'Yesterday';
  return date.toLocaleDateString(undefined, { day: 'numeric', month: 'short', year: 'numeric' });
}

function timeLabel(iso: string): string {
  return new Date(iso).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
}

type Row =
  | { kind: 'day'; key: string; label: string }
  | { kind: 'message'; key: string; message: SupportMessage; first: boolean; last: boolean }
  | { kind: 'pending'; key: string; item: PendingMessage };

function Thread({
  messages,
  pending,
  agentReadAt,
  onRetry,
}: {
  messages: SupportMessage[];
  pending: PendingMessage[];
  agentReadAt: string | null;
  onRetry: (item: PendingMessage) => void;
}) {
  // Messages from one side within a few minutes are grouped: one name, one
  // avatar, tighter spacing - how a conversation actually reads.
  const rows: Row[] = [];
  let lastDay = '';
  messages.forEach((message, index) => {
    const day = new Date(message.createdAt).toDateString();
    if (day !== lastDay) {
      rows.push({ kind: 'day', key: 'd-' + day, label: dayLabel(message.createdAt) });
      lastDay = day;
    }
    const prev = messages[index - 1];
    const next = messages[index + 1];
    const near = (a?: SupportMessage) =>
      !!a &&
      a.sender === message.sender &&
      Math.abs(new Date(a.createdAt).getTime() - new Date(message.createdAt).getTime()) < 5 * 60_000 &&
      new Date(a.createdAt).toDateString() === day;
    rows.push({ kind: 'message', key: message.id, message, first: !near(prev), last: !near(next) });
  });
  for (const item of pending) rows.push({ kind: 'pending', key: item.tempId, item });

  const readUpTo = agentReadAt ? new Date(agentReadAt).getTime() : 0;

  return (
    <div className="space-y-1">
      {rows.map((row) => {
        if (row.kind === 'day') {
          return (
            <div key={row.key} className="flex items-center gap-3 py-3">
              <span className="h-px flex-1 bg-surface-800" />
              <span className="text-[10px] font-semibold uppercase tracking-wider text-content-400">{row.label}</span>
              <span className="h-px flex-1 bg-surface-800" />
            </div>
          );
        }

        if (row.kind === 'pending') {
          return (
            <div key={row.key} className="flex justify-end pt-1">
              <div className="max-w-[78%] sm:max-w-[68%]">
                <div
                  className={clsx(
                    'whitespace-pre-wrap break-words rounded-2xl rounded-br-md px-3.5 py-2 text-sm',
                    row.item.failed ? 'bg-down/15 text-content-100 ring-1 ring-down/40' : 'bg-accent/70 text-surface-950',
                  )}
                >
                  {row.item.body}
                </div>
                <div className="mt-0.5 flex items-center justify-end gap-1 text-[10px] text-content-400">
                  {row.item.failed ? (
                    <button type="button" onClick={() => onRetry(row.item)} className="flex items-center gap-1 font-semibold text-down">
                      <RotateCcw className="h-3 w-3" />
                      Not sent — tap to retry
                    </button>
                  ) : (
                    <>
                      <Clock className="h-3 w-3" />
                      Sending
                    </>
                  )}
                </div>
              </div>
            </div>
          );
        }

        const { message, first, last } = row;

        if (message.sender === 'SYSTEM') {
          return (
            <div key={row.key} className="flex justify-center py-2">
              <span className="rounded-full bg-surface-850 px-3 py-1 text-[11px] text-content-400">
                {message.body} · {timeLabel(message.createdAt)}
              </span>
            </div>
          );
        }

        const mine = message.sender === 'USER';
        const read = mine && new Date(message.createdAt).getTime() <= readUpTo;

        return (
          <div key={row.key} className={clsx('flex gap-2', mine ? 'justify-end' : 'justify-start', first && 'pt-2')}>
            {!mine ? (
              <div className="w-7 shrink-0">
                {last ? (
                  <span className="grid h-7 w-7 place-items-center rounded-full bg-surface-700 text-[11px] font-bold text-content-100">
                    {message.senderName.slice(0, 1).toUpperCase()}
                  </span>
                ) : null}
              </div>
            ) : null}

            <div className={clsx('max-w-[78%] sm:max-w-[68%]', mine && 'flex flex-col items-end')}>
              {!mine && first ? (
                <span className="mb-0.5 ml-1 text-[11px] font-semibold text-content-300">
                  {message.senderName} <span className="font-normal text-content-400">· Support</span>
                </span>
              ) : null}
              <div
                className={clsx(
                  'whitespace-pre-wrap break-words px-3.5 py-2 text-sm leading-relaxed',
                  mine
                    ? 'rounded-2xl bg-accent text-surface-950 shadow-raised'
                    : 'rounded-2xl bg-surface-800 text-content-100',
                  mine && last && 'rounded-br-md',
                  !mine && last && 'rounded-bl-md',
                )}
              >
                {message.body}
              </div>
              {last ? (
                <div className={clsx('mt-0.5 flex items-center gap-1 px-1 text-[10px] text-content-400', mine && 'justify-end')}>
                  {timeLabel(message.createdAt)}
                  {mine ? (
                    read ? (
                      <span className="flex items-center gap-0.5 text-accent" title="Read by support">
                        <CheckCheck className="h-3.5 w-3.5" />
                        Read
                      </span>
                    ) : (
                      <Check className="h-3.5 w-3.5" aria-label="Delivered" />
                    )
                  ) : null}
                </div>
              ) : null}
            </div>
          </div>
        );
      })}
    </div>
  );
}

function TypingBubble({ name }: { name: string }) {
  return (
    <div className="flex items-end gap-2 pt-3" aria-live="polite">
      <span className="grid h-7 w-7 place-items-center rounded-full bg-surface-700 text-[11px] font-bold text-content-100">
        {name.slice(0, 1).toUpperCase()}
      </span>
      <div>
        <div className="flex items-center gap-1 rounded-2xl rounded-bl-md bg-surface-800 px-3.5 py-3">
          {[0, 1, 2].map((i) => (
            <span
              key={i}
              className="h-1.5 w-1.5 animate-bounce rounded-full bg-content-300"
              style={{ animationDelay: i * 140 + 'ms' }}
            />
          ))}
        </div>
        <span className="ml-1 text-[10px] text-content-400">{name} is typing…</span>
      </div>
    </div>
  );
}
