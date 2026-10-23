import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import clsx from 'clsx';
import {
  ArrowLeft,
  CheckCheck,
  CheckCircle2,
  Clock,
  Headset,
  Inbox,
  RotateCcw,
  Search,
  SendHorizontal,
  UserCheck,
  UserRound,
  Zap,
} from 'lucide-react';
import { api } from '../services/api';
import { socket, WsEvents } from '../websocket/client';
import { useAuthStore } from '../stores/authStore';
import { Spinner } from '../components/ui';
import type { SupportConversation, SupportMessage } from '../types/api';

/**
 * Support desk.
 *
 * An inbox of every customer conversation on the left, the open thread on the
 * right. Everything is live: new messages, read receipts and "customer is
 * typing" arrive over the websocket, and the inbox re-sorts itself so an
 * unanswered customer rises to the top.
 */

type Filter = 'OPEN' | 'RESOLVED' | 'ALL';

const QUICK_REPLIES = [
  'Hi, thanks for reaching out. Let me look into this for you.',
  'Could you share the UTR / transaction reference so I can check it?',
  'Your deposit has been verified and credited to your wallet.',
  'Withdrawals are processed after review; I will update you here as soon as it is paid.',
  'Is there anything else I can help you with?',
];

function customerName(c: SupportConversation): string {
  return c.user?.fullName?.trim() || c.user?.username || 'Customer';
}

function relative(iso: string | null): string {
  if (!iso) return '';
  const diff = Date.now() - new Date(iso).getTime();
  if (diff < 60_000) return 'now';
  if (diff < 3_600_000) return Math.floor(diff / 60_000) + 'm';
  if (diff < 86_400_000) return Math.floor(diff / 3_600_000) + 'h';
  return new Date(iso).toLocaleDateString(undefined, { day: 'numeric', month: 'short' });
}

function timeLabel(iso: string): string {
  return new Date(iso).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
}

export function SupportPage() {
  const [params, setParams] = useSearchParams();
  const selectedId = params.get('c');
  const [filter, setFilter] = useState<Filter>('OPEN');
  const [mine, setMine] = useState(false);
  const [search, setSearch] = useState('');

  const listQuery = useQuery({
    queryKey: ['support', 'list', filter, mine, search],
    queryFn: () =>
      api.support.list({
        limit: 50,
        status: filter === 'ALL' ? undefined : filter,
        mine: mine || undefined,
        search: search.trim() || undefined,
      }),
    refetchInterval: 30_000,
  });

  const select = (id: string | null) => {
    const next = new URLSearchParams(params);
    if (id) next.set('c', id);
    else next.delete('c');
    setParams(next, { replace: true });
  };

  const conversations = listQuery.data?.items ?? [];

  return (
    <div className="flex h-[calc(100vh-7rem)] min-h-[520px] overflow-hidden rounded-xl border border-surface-700 bg-surface-900 shadow-panel">
      {/* Inbox */}
      <aside
        className={clsx(
          'flex w-full shrink-0 flex-col border-r border-surface-800 md:w-[340px]',
          selectedId && 'hidden md:flex',
        )}
      >
        <div className="border-b border-surface-800 p-3">
          <div className="flex items-center gap-2">
            <Headset className="h-4 w-4 text-accent" />
            <h1 className="text-sm font-semibold text-content-100">Support inbox</h1>
            {listQuery.data?.unreadTotal ? (
              <span className="ml-auto rounded-full bg-down-solid px-2 py-0.5 text-[10px] font-bold text-white">
                {listQuery.data.unreadTotal} unread
              </span>
            ) : null}
          </div>
          <div className="relative mt-3">
            <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-content-400" />
            <input
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Search name, email or username"
              className="field pl-8 text-xs"
            />
          </div>
          <div className="mt-2 flex items-center gap-1">
            {(['OPEN', 'RESOLVED', 'ALL'] as Filter[]).map((f) => (
              <button
                key={f}
                type="button"
                onClick={() => setFilter(f)}
                className={clsx(
                  'rounded-md px-2.5 py-1 text-[11px] font-semibold transition',
                  filter === f ? 'bg-accent-solid text-on-accent' : 'text-content-400 hover:bg-surface-850 hover:text-content-100',
                )}
              >
                {f === 'ALL' ? 'All' : f === 'OPEN' ? 'Open' : 'Resolved'}
              </button>
            ))}
            <label className="ml-auto flex cursor-pointer items-center gap-1.5 text-[11px] text-content-400">
              <input type="checkbox" checked={mine} onChange={(e) => setMine(e.target.checked)} className="accent-current" />
              Mine
            </label>
          </div>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto">
          {listQuery.isLoading ? (
            <div className="grid h-40 place-items-center">
              <Spinner className="text-accent" />
            </div>
          ) : conversations.length === 0 ? (
            <div className="px-6 py-14 text-center">
              <Inbox className="mx-auto h-7 w-7 text-content-400" />
              <p className="mt-2 text-sm text-content-300">No conversations here</p>
            </div>
          ) : (
            conversations.map((c) => (
              <button
                key={c.id}
                type="button"
                onClick={() => select(c.id)}
                className={clsx(
                  'flex w-full items-start gap-3 border-b border-surface-800/70 px-3 py-3 text-left transition',
                  c.id === selectedId ? 'bg-accent/10' : 'hover:bg-surface-850',
                )}
              >
                <span className="grid h-9 w-9 shrink-0 place-items-center rounded-full bg-surface-700 text-xs font-bold text-content-100">
                  {customerName(c).slice(0, 1).toUpperCase()}
                </span>
                <span className="min-w-0 flex-1">
                  <span className="flex items-center gap-2">
                    <span className={clsx('truncate text-sm', c.unreadByAgent ? 'font-bold text-content-100' : 'font-medium text-content-200')}>
                      {customerName(c)}
                    </span>
                    <span className="ml-auto shrink-0 text-[10px] text-content-400">{relative(c.lastMessageAt)}</span>
                  </span>
                  <span className="mt-0.5 flex items-center gap-2">
                    <span className={clsx('truncate text-xs', c.unreadByAgent ? 'text-content-200' : 'text-content-400')}>
                      {c.lastSender === 'AGENT' ? 'You: ' : ''}
                      {c.lastMessagePreview}
                    </span>
                    {c.unreadByAgent ? (
                      <span className="ml-auto grid h-5 min-w-5 shrink-0 place-items-center rounded-full bg-accent-solid px-1.5 text-[10px] font-bold text-on-accent">
                        {c.unreadByAgent}
                      </span>
                    ) : null}
                  </span>
                  <span className="mt-1 flex items-center gap-2 text-[10px] text-content-400">
                    {c.status === 'RESOLVED' ? (
                      <span className="flex items-center gap-1 text-up">
                        <CheckCircle2 className="h-3 w-3" /> Resolved
                      </span>
                    ) : null}
                    {c.assignedName ? (
                      <span className="flex items-center gap-1">
                        <UserCheck className="h-3 w-3" /> {c.assignedName}
                      </span>
                    ) : (
                      <span className="text-warn">Unassigned</span>
                    )}
                  </span>
                </span>
              </button>
            ))
          )}
        </div>
      </aside>

      {/* Thread */}
      <section className={clsx('min-w-0 flex-1 flex-col', selectedId ? 'flex' : 'hidden md:flex')}>
        {selectedId ? (
          <ThreadView key={selectedId} conversationId={selectedId} onBack={() => select(null)} />
        ) : (
          <div className="grid h-full place-items-center text-center">
            <div>
              <Headset className="mx-auto h-9 w-9 text-content-400" />
              <p className="mt-3 text-sm text-content-300">Select a conversation to reply</p>
              <p className="mt-1 text-xs text-content-400">Unanswered customers are listed first.</p>
            </div>
          </div>
        )}
      </section>
    </div>
  );
}

function ThreadView({ conversationId, onBack }: { conversationId: string; onBack: () => void }) {
  const queryClient = useQueryClient();
  const me = useAuthStore((s) => s.user);
  const [conversation, setConversation] = useState<SupportConversation | null>(null);
  const [messages, setMessages] = useState<SupportMessage[]>([]);
  const [hasMore, setHasMore] = useState(false);
  const [draft, setDraft] = useState('');
  const [sending, setSending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [customerTypingUntil, setCustomerTypingUntil] = useState(0);
  const [now, setNow] = useState(Date.now());
  const [showQuick, setShowQuick] = useState(false);

  const scrollRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const stick = useRef(true);
  const lastTyping = useRef(0);

  const merge = useCallback((incoming: SupportMessage[]) => {
    setMessages((prev) => {
      const map = new Map(prev.map((m) => [m.id, m]));
      for (const m of incoming) map.set(m.id, m);
      return [...map.values()].sort((a, b) => a.createdAt.localeCompare(b.createdAt));
    });
  }, []);

  useEffect(() => {
    let cancelled = false;
    api.support
      .thread(conversationId)
      .then((thread) => {
        if (cancelled) return;
        setConversation(thread.conversation);
        merge(thread.items);
        setHasMore(thread.hasMore);
      })
      .catch(() => !cancelled && setError('Could not load this conversation'));
    return () => {
      cancelled = true;
    };
  }, [conversationId, merge]);

  // Live updates for this thread only.
  useEffect(() => {
    const offs = [
      socket.on(WsEvents.SUPPORT_MESSAGE, (payload) => {
        const m = payload as SupportMessage;
        if (m.conversationId !== conversationId) return;
        merge([m]);
        if (m.sender === 'USER') setCustomerTypingUntil(0);
      }),
      socket.on(WsEvents.SUPPORT_CONVERSATION, (payload) => {
        const c = payload as SupportConversation;
        if (c.id === conversationId) setConversation((prev) => ({ ...prev, ...c, user: c.user ?? prev?.user }));
      }),
      socket.on(WsEvents.SUPPORT_TYPING, (payload) => {
        const t = payload as { conversationId: string; side: string };
        if (t.conversationId === conversationId && t.side === 'USER') setCustomerTypingUntil(Date.now() + 5000);
      }),
    ];
    return () => offs.forEach((off) => off());
  }, [conversationId, merge]);

  useEffect(() => {
    if (!customerTypingUntil) return;
    const timer = window.setInterval(() => setNow(Date.now()), 500);
    return () => window.clearInterval(timer);
  }, [customerTypingUntil]);
  const customerTyping = customerTypingUntil > now;

  // Opening a thread with unread customer messages marks them read.
  const unread = conversation?.unreadByAgent ?? 0;
  useEffect(() => {
    if (!unread) return;
    const timer = window.setTimeout(() => {
      api.support
        .markRead(conversationId)
        .then((c) => {
          setConversation((prev) => ({ ...prev, ...c, user: c.user ?? prev?.user }));
          void queryClient.invalidateQueries({ queryKey: ['support'] });
        })
        .catch(() => undefined);
    }, 300);
    return () => window.clearTimeout(timer);
  }, [unread, conversationId, queryClient]);

  useLayoutEffect(() => {
    const el = scrollRef.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  }, [messages.length, customerTyping]);

  useLayoutEffect(() => {
    const el = inputRef.current;
    if (!el) return;
    el.style.height = 'auto';
    el.style.height = Math.min(el.scrollHeight, 150) + 'px';
  }, [draft]);

  async function loadOlder() {
    const oldest = messages[0];
    if (!oldest) return;
    const el = scrollRef.current;
    const before = el?.scrollHeight ?? 0;
    const thread = await api.support.thread(conversationId, { before: oldest.id });
    merge(thread.items);
    setHasMore(thread.hasMore);
    requestAnimationFrame(() => {
      if (el) el.scrollTop += el.scrollHeight - before;
    });
  }

  async function send(text = draft) {
    const body = text.trim();
    if (!body || sending) return;
    setSending(true);
    setError(null);
    try {
      const result = await api.support.send(conversationId, body);
      merge([result.message]);
      setConversation((prev) => ({ ...prev, ...result.conversation, user: result.conversation.user ?? prev?.user }));
      setDraft('');
      stick.current = true;
      void queryClient.invalidateQueries({ queryKey: ['support'] });
    } catch {
      setError('Message not sent. Try again.');
    } finally {
      setSending(false);
      inputRef.current?.focus();
    }
  }

  async function update(change: { status?: 'OPEN' | 'RESOLVED'; assignToMe?: boolean }) {
    try {
      const c = await api.support.update(conversationId, change);
      setConversation((prev) => ({ ...prev, ...c, user: c.user ?? prev?.user }));
      void queryClient.invalidateQueries({ queryKey: ['support'] });
    } catch {
      setError('Could not update the conversation');
    }
  }

  function onKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) {
      event.preventDefault();
      void send();
    }
  }

  function onChange(value: string) {
    setDraft(value);
    const t = Date.now();
    if (value.trim() && t - lastTyping.current > 3000) {
      lastTyping.current = t;
      api.support.typing(conversationId).catch(() => undefined);
    }
  }

  const seenUpTo = conversation?.userReadAt ? new Date(conversation.userReadAt).getTime() : 0;
  const lastAgentId = useMemo(() => [...messages].reverse().find((m) => m.sender === 'AGENT')?.id, [messages]);

  if (!conversation) {
    return (
      <div className="grid h-full place-items-center">
        {error ? <p className="text-sm text-down">{error}</p> : <Spinner className="text-accent" />}
      </div>
    );
  }

  const resolved = conversation.status === 'RESOLVED';
  const assignedToMe = conversation.assignedTo && conversation.assignedTo === me?.id;

  return (
    <>
      <header className="flex shrink-0 items-center gap-3 border-b border-surface-800 px-4 py-3">
        <button type="button" onClick={onBack} className="rounded-md p-1 text-content-400 hover:bg-surface-850 md:hidden" aria-label="Back">
          <ArrowLeft className="h-4 w-4" />
        </button>
        <span className="grid h-9 w-9 place-items-center rounded-full bg-surface-700 text-xs font-bold text-content-100">
          {customerName(conversation).slice(0, 1).toUpperCase()}
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className="truncate text-sm font-semibold text-content-100">{customerName(conversation)}</span>
            <span
              className={clsx(
                'rounded-full px-2 py-0.5 text-[10px] font-bold',
                resolved ? 'bg-surface-800 text-content-300' : 'bg-up/15 text-up',
              )}
            >
              {resolved ? 'Resolved' : 'Open'}
            </span>
          </div>
          <div className="truncate text-[11px] text-content-400">
            {conversation.user?.email}
            {conversation.assignedName ? ' · handled by ' + conversation.assignedName : ' · unassigned'}
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-1.5">
          <Link
            to={'/admin/users/' + conversation.userId}
            className="hidden items-center gap-1 rounded-md border border-surface-700 px-2.5 py-1.5 text-xs text-content-300 hover:text-content-100 sm:flex"
          >
            <UserRound className="h-3.5 w-3.5" /> Profile
          </Link>
          {!assignedToMe ? (
            <button
              type="button"
              onClick={() => update({ assignToMe: true })}
              className="flex items-center gap-1 rounded-md border border-surface-700 px-2.5 py-1.5 text-xs text-content-300 hover:text-content-100"
            >
              <UserCheck className="h-3.5 w-3.5" /> Assign to me
            </button>
          ) : null}
          {resolved ? (
            <button
              type="button"
              onClick={() => update({ status: 'OPEN' })}
              className="flex items-center gap-1 rounded-md border border-surface-700 px-2.5 py-1.5 text-xs text-content-300 hover:text-content-100"
            >
              <RotateCcw className="h-3.5 w-3.5" /> Reopen
            </button>
          ) : (
            <button
              type="button"
              onClick={() => update({ status: 'RESOLVED' })}
              className="flex items-center gap-1 rounded-md bg-up/15 px-2.5 py-1.5 text-xs font-semibold text-up hover:bg-up/25"
            >
              <CheckCircle2 className="h-3.5 w-3.5" /> Resolve
            </button>
          )}
        </div>
      </header>

      <div ref={scrollRef} onScroll={(e) => {
        const el = e.currentTarget;
        stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
      }} className="min-h-0 flex-1 space-y-2 overflow-y-auto px-4 py-4">
        {hasMore ? (
          <div className="text-center">
            <button type="button" onClick={loadOlder} className="rounded-full border border-surface-700 px-3 py-1 text-xs text-content-300 hover:text-content-100">
              Load earlier messages
            </button>
          </div>
        ) : null}

        {messages.map((m) => {
          if (m.sender === 'SYSTEM') {
            return (
              <div key={m.id} className="flex justify-center py-1">
                <span className="rounded-full bg-surface-850 px-3 py-1 text-[11px] text-content-400">
                  {m.body} · {timeLabel(m.createdAt)}
                </span>
              </div>
            );
          }
          const agent = m.sender === 'AGENT';
          const seen = agent && new Date(m.createdAt).getTime() <= seenUpTo;
          return (
            <div key={m.id} className={clsx('flex', agent ? 'justify-end' : 'justify-start')}>
              <div className={clsx('max-w-[75%]', agent && 'flex flex-col items-end')}>
                <div
                  className={clsx(
                    'whitespace-pre-wrap break-words rounded-2xl px-3.5 py-2 text-sm leading-relaxed',
                    agent ? 'rounded-br-md bg-accent-solid text-on-accent' : 'rounded-bl-md bg-surface-800 text-content-100',
                  )}
                >
                  {m.body}
                </div>
                <div className="mt-0.5 flex items-center gap-1 px-1 text-[10px] text-content-400">
                  {agent ? m.senderName + ' · ' : ''}
                  {timeLabel(m.createdAt)}
                  {agent && m.id === lastAgentId ? (
                    seen ? (
                      <span className="flex items-center gap-0.5 text-accent">
                        <CheckCheck className="h-3 w-3" /> Seen
                      </span>
                    ) : (
                      <span className="flex items-center gap-0.5">
                        <Clock className="h-3 w-3" /> Delivered
                      </span>
                    )
                  ) : null}
                </div>
              </div>
            </div>
          );
        })}

        {customerTyping ? (
          <div className="flex items-center gap-2 pt-1 text-[11px] text-content-400">
            <span className="flex items-center gap-1 rounded-2xl rounded-bl-md bg-surface-800 px-3 py-2.5">
              {[0, 1, 2].map((i) => (
                <span key={i} className="h-1.5 w-1.5 animate-bounce rounded-full bg-content-300" style={{ animationDelay: i * 140 + 'ms' }} />
              ))}
            </span>
            {customerName(conversation)} is typing…
          </div>
        ) : null}
      </div>

      <footer className="shrink-0 border-t border-surface-800 p-3">
        {showQuick ? (
          <div className="mb-2 flex flex-wrap gap-1.5">
            {QUICK_REPLIES.map((reply) => (
              <button
                key={reply}
                type="button"
                onClick={() => {
                  setDraft(reply);
                  setShowQuick(false);
                  inputRef.current?.focus();
                }}
                className="max-w-full truncate rounded-full border border-surface-700 bg-surface-850 px-3 py-1 text-[11px] text-content-300 hover:border-accent/50 hover:text-content-100"
              >
                {reply}
              </button>
            ))}
          </div>
        ) : null}
        {error ? <p className="mb-2 text-xs text-down">{error}</p> : null}
        <div className="flex items-end gap-2 rounded-xl border border-surface-700 bg-surface-850 p-1.5 focus-within:border-accent/60">
          <button
            type="button"
            onClick={() => setShowQuick((v) => !v)}
            title="Quick replies"
            aria-label="Quick replies"
            className={clsx('grid h-9 w-9 shrink-0 place-items-center rounded-lg transition', showQuick ? 'bg-accent/15 text-accent' : 'text-content-400 hover:text-content-100')}
          >
            <Zap className="h-4 w-4" />
          </button>
          <textarea
            ref={inputRef}
            rows={1}
            value={draft}
            onChange={(e) => onChange(e.target.value)}
            onKeyDown={onKeyDown}
            maxLength={2000}
            placeholder={resolved ? 'Reply to reopen the conversation…' : 'Reply to ' + customerName(conversation) + '…'}
            className="max-h-36 min-h-[36px] flex-1 resize-none bg-transparent py-2 text-sm text-content-100 outline-none focus-visible:outline-none placeholder:text-content-400"
          />
          <button
            type="button"
            onClick={() => void send()}
            disabled={!draft.trim() || sending}
            aria-label="Send"
            className="grid h-9 w-9 shrink-0 place-items-center rounded-lg bg-accent-solid text-on-accent transition hover:brightness-105 disabled:opacity-40"
          >
            {sending ? <Spinner /> : <SendHorizontal className="h-4 w-4" />}
          </button>
        </div>
        <p className="mt-1 px-1 text-[10px] text-content-400">Enter to send · Shift + Enter for a new line</p>
      </footer>
    </>
  );
}
