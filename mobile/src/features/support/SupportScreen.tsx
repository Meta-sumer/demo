import { useCallback, useEffect, useMemo, useRef, useState, type ComponentType } from 'react';
import {
  AppState,
  FlatList,
  Keyboard,
  KeyboardAvoidingView,
  Pressable,
  ScrollView,
  TextInput,
  View,
} from 'react-native';
import { router, useFocusEffect } from 'expo-router';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import Animated, {
  useAnimatedStyle,
  useSharedValue,
  withDelay,
  withRepeat,
  withSequence,
  withTiming,
} from 'react-native-reanimated';
import {
  ArrowDownToLine,
  ArrowUpFromLine,
  Check,
  CheckCheck,
  ChevronLeft,
  CircleAlert,
  Clock,
  Headset,
  LineChart,
  Lock,
  RotateCcw,
  SendHorizontal,
  ShieldCheck,
  UserRound,
} from 'lucide-react-native';
import { api } from '../../services/api';
import { useAuthStore } from '../../stores/authStore';
import { useSupportStore, type PendingMessage } from '../../stores/supportStore';
import type { SupportMessage } from '../../types/api';
import { cn } from '../../utils/cn';
import { useTokenColor } from '../../utils/theme-colors';
import { Icon, IconButton, Spinner, Text, type IconGlyphProps } from '../../components/ui';

/**
 * Live support chat.
 *
 * One continuous thread with the support desk. Messages appear the moment
 * either side sends them (websocket), the customer sees when the desk has read
 * what they wrote, and when an agent is typing. Sending is optimistic - the
 * bubble appears immediately and is confirmed, or offered for retry, when the
 * server answers.
 *
 * The thread is an inverted list, so the newest message sits on the composer
 * and the composer rides on the keyboard.
 */

const MAX_LENGTH = 2000;
/** How often a "typing" signal may be repeated while the customer types. */
const TYPING_THROTTLE_MS = 3000;
/** Within this distance of the newest message, new content keeps the view pinned to it. */
const STICK_PX = 80;

const TOPICS: Array<{ icon: ComponentType<IconGlyphProps>; label: string; text: string }> = [
  { icon: ArrowDownToLine, label: 'Deposit', text: 'Hi, I have a question about a deposit.' },
  { icon: ArrowUpFromLine, label: 'Withdrawal', text: 'Hi, I need help with a withdrawal.' },
  { icon: LineChart, label: 'A trade', text: 'Hi, I have a question about one of my trades.' },
  { icon: UserRound, label: 'My account', text: 'Hi, I need help with my account.' },
];

export function SupportScreen() {
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
  const [inputFocused, setInputFocused] = useState(false);
  const [keyboardShown, setKeyboardShown] = useState(false);

  const listRef = useRef<FlatList<Row>>(null);
  const inputRef = useRef<TextInput>(null);
  const lastTypingSent = useRef(0);
  const insets = useSafeAreaInsets();
  const placeholderColor = useTokenColor('content-400');
  const selectionColor = useTokenColor('accent');

  /* ------------------------------------------------------------ loading */

  const load = useCallback(() => {
    setLoadError(false);
    api.support
      .thread()
      .then(setThread)
      .catch(() => setLoadError(true));
  }, [setThread]);

  useEffect(() => {
    load();
  }, [load]);

  // The typing indicator lapses on its own; a clock tick lets it disappear.
  useEffect(() => {
    if (!agentTypingUntil) return;
    const timer = setInterval(() => setNow(Date.now()), 500);
    return () => clearInterval(timer);
  }, [agentTypingUntil]);
  const agentTyping = agentTypingUntil !== null && agentTypingUntil > now;

  /* --------------------------------------------------------- read state */

  // While the screen is open, on top and the app in the foreground, anything
  // the desk sends is read - including what arrived while the app was in the
  // background, the moment it comes back.
  const [focused, setFocused] = useState(true);
  useFocusEffect(
    useCallback(() => {
      setFocused(true);
      return () => setFocused(false);
    }, []),
  );
  const [appActive, setAppActive] = useState(AppState.currentState === 'active');
  useEffect(() => {
    const subscription = AppState.addEventListener('change', (state) => setAppActive(state === 'active'));
    return () => subscription.remove();
  }, []);

  const unread = conversation?.unreadByUser ?? 0;
  useEffect(() => {
    if (!loaded || unread === 0 || !appActive || !focused) return;
    const timer = setTimeout(() => {
      api.support.markRead().then(setConversation).catch(() => undefined);
    }, 400);
    return () => clearTimeout(timer);
  }, [loaded, unread, appActive, focused, setConversation]);

  /* ----------------------------------------------------------- keyboard */

  useEffect(() => {
    const show = Keyboard.addListener('keyboardDidShow', () => setKeyboardShown(true));
    const hide = Keyboard.addListener('keyboardDidHide', () => setKeyboardShown(false));
    return () => {
      show.remove();
      hide.remove();
    };
  }, []);

  /* ---------------------------------------------------------- scrolling */

  // New content keeps the view pinned to the newest message, unless the
  // customer has scrolled up to read history - then it is left alone. The
  // list is inverted, so "the bottom" is offset 0 and the list's
  // maintainVisibleContentPosition does the pinning; older pages land at the
  // far end and never move what is on screen.
  const scrollToBottom = useCallback((animated: boolean) => {
    listRef.current?.scrollToOffset({ offset: 0, animated });
  }, []);

  async function loadOlder() {
    const oldest = messages[0];
    if (!oldest || loadingOlder) return;
    setLoadingOlder(true);
    try {
      prependOlder(await api.support.thread({ before: oldest.id }));
    } catch {
      /* the button stays, so the customer can try again */
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
    void deliver(item);
    // Sending always brings the newest message back into view.
    requestAnimationFrame(() => scrollToBottom(true));
    inputRef.current?.focus();
  }

  function retry(item: PendingMessage) {
    resolvePending(item.tempId);
    const fresh = { ...item, tempId: item.tempId + 'r', failed: false };
    addPending(fresh);
    void deliver(fresh);
  }

  function onChange(value: string) {
    setDraft(value);
    const t = Date.now();
    if (value.trim() && t - lastTypingSent.current > TYPING_THROTTLE_MS) {
      lastTypingSent.current = t;
      api.support.typing().catch(() => undefined);
    }
  }

  /* ------------------------------------------------------------- render */

  const resolved = conversation?.status === 'RESOLVED';
  const agentName = conversation?.assignedName ?? null;
  const empty = loaded && messages.length === 0 && pending.length === 0;
  const readUpTo = conversation?.agentReadAt ? new Date(conversation.agentReadAt).getTime() : 0;
  // Newest first, for the inverted list.
  const rows = useMemo(() => buildRows(messages, pending).reverse(), [messages, pending]);

  return (
    <View className="flex-1 bg-surface-950">
      <SupportHeader agentName={agentName} />

      <KeyboardAvoidingView behavior="padding" style={{ flex: 1 }}>
        {/* Thread */}
        <View className="flex-1">
          {!loaded && !loadError ? (
            <View className="flex-1 items-center justify-center">
              <Spinner />
            </View>
          ) : loadError && !loaded ? (
            <View className="flex-1 items-center justify-center px-6">
              <Icon as={CircleAlert} size={24} className="text-down" />
              <Text className="mt-2 text-center text-sm text-content-300">Could not load the conversation.</Text>
              <Pressable onPress={load} accessibilityRole="button" className="mt-3 px-2 py-1 active:opacity-70">
                <Text className="text-sm font-semibold text-accent">Try again</Text>
              </Pressable>
            </View>
          ) : empty ? (
            <ScrollView
              contentContainerClassName="grow justify-center px-4 py-6"
              keyboardShouldPersistTaps="handled"
            >
              <Welcome name={user?.fullName ?? user?.username ?? null} onTopic={(text) => send(text)} />
            </ScrollView>
          ) : (
            <FlatList
              ref={listRef}
              inverted
              data={rows}
              keyExtractor={(row) => row.key}
              renderItem={({ item }) => <ThreadRow row={item} readUpTo={readUpTo} onRetry={retry} />}
              maintainVisibleContentPosition={{ minIndexForVisible: 0, autoscrollToTopThreshold: STICK_PX }}
              keyboardShouldPersistTaps="handled"
              keyboardDismissMode="interactive"
              contentContainerClassName="px-3 py-4"
              // Inverted: the header sits under the newest message, the footer above the oldest.
              ListHeaderComponent={agentTyping ? <TypingBubble name={agentTypingName ?? 'Support'} /> : null}
              ListFooterComponent={
                hasMore ? (
                  <View className="mb-4 items-center">
                    <Pressable
                      onPress={loadOlder}
                      disabled={loadingOlder}
                      accessibilityRole="button"
                      className="rounded-full border border-surface-700 bg-surface-850 px-3 py-1 active:opacity-80"
                    >
                      <Text className="text-xs font-medium text-content-300">
                        {loadingOlder ? 'Loading…' : 'Load earlier messages'}
                      </Text>
                    </Pressable>
                  </View>
                ) : null
              }
            />
          )}
        </View>

        {/* Composer */}
        <View
          className="border-t border-surface-800 bg-surface-900 px-3 pt-2.5"
          style={{ paddingBottom: keyboardShown ? 10 : Math.max(insets.bottom, 12) }}
        >
          {resolved ? (
            <Text className="mb-2 text-center text-[11px] text-content-400">
              This conversation was resolved. Send a message to reopen it.
            </Text>
          ) : null}
          <View
            className={cn(
              'flex-row items-end gap-2 rounded-2xl border bg-surface-850 p-1.5 pl-3.5',
              inputFocused ? 'border-accent/60' : 'border-surface-700',
            )}
          >
            <TextInput
              ref={inputRef}
              multiline
              value={draft}
              onChangeText={onChange}
              onFocus={() => setInputFocused(true)}
              onBlur={() => setInputFocused(false)}
              placeholder="Type your message…"
              placeholderTextColor={placeholderColor}
              selectionColor={selectionColor}
              accessibilityLabel="Message"
              maxLength={MAX_LENGTH}
              editable={loaded}
              textAlignVertical="center"
              className="max-h-40 min-h-[40px] flex-1 py-2.5 text-sm text-content-100"
            />
            <Pressable
              onPress={() => send()}
              disabled={!draft.trim() || !loaded}
              accessibilityRole="button"
              accessibilityLabel="Send"
              className={cn(
                'h-10 w-10 items-center justify-center rounded-xl bg-accent active:opacity-80',
                (!draft.trim() || !loaded) && 'opacity-40',
              )}
            >
              <Icon as={SendHorizontal} size={18} className="text-surface-950" />
            </Pressable>
          </View>
          <View className="mt-1.5 flex-row items-center justify-between px-1">
            <View className="flex-row items-center gap-1">
              <Icon as={Lock} size={12} className="text-content-400" />
              <Text className="text-[10px] text-content-400">Return for a new line · tap Send to post</Text>
            </View>
            {draft.length > MAX_LENGTH - 200 ? (
              <Text className={cn('text-[10px] tnum', draft.length >= MAX_LENGTH ? 'text-down' : 'text-content-400')}>
                {draft.length} / {MAX_LENGTH}
              </Text>
            ) : null}
          </View>
        </View>
      </KeyboardAvoidingView>
    </View>
  );
}

/* ================================================================ pieces */

/** Back, who the customer is talking to, and that it is a live chat. */
function SupportHeader({ agentName }: { agentName: string | null }) {
  const insets = useSafeAreaInsets();
  return (
    <View
      className="flex-row items-center gap-2 border-b border-surface-800 bg-surface-900 pb-2.5 pl-1.5 pr-4"
      style={{ paddingTop: insets.top + 6 }}
    >
      <IconButton
        icon={ChevronLeft}
        size={22}
        accessibilityLabel="Back"
        iconClassName="text-content-100"
        onPress={() => {
          if (router.canGoBack()) router.back();
          else router.replace('/account');
        }}
      />
      <View className="relative h-11 w-11 items-center justify-center rounded-2xl bg-accent">
        <Icon as={Headset} size={20} className="text-surface-950" />
        <View className="absolute -bottom-0.5 -right-0.5 h-3 w-3 rounded-full border-2 border-surface-900 bg-up" />
      </View>
      <View className="min-w-0 flex-1">
        <View className="flex-row items-center gap-2">
          <Text accessibilityRole="header" className="text-base font-bold text-content-100">
            Support
          </Text>
          <View className="rounded-full bg-accent/15 px-2 py-0.5">
            <Text className="text-[10px] font-bold uppercase tracking-wider text-accent">Live chat</Text>
          </View>
        </View>
        <Text numberOfLines={1} className="text-xs text-content-400">
          {agentName ? (
            <>
              You are chatting with <Text className="text-xs font-semibold text-content-200">{agentName}</Text>
            </>
          ) : (
            'Write to us and a member of the support team will reply here'
          )}
        </Text>
      </View>
    </View>
  );
}

function Welcome({ name, onTopic }: { name: string | null; onTopic: (text: string) => void }) {
  return (
    <View className="w-full items-center self-center">
      <View className="h-16 w-16 items-center justify-center rounded-3xl bg-accent/15">
        <Icon as={Headset} size={32} className="text-accent" />
      </View>
      <Text className="mt-5 text-center text-xl font-bold text-content-100">
        {name ? 'Hi ' + name.split(' ')[0] + ', how can we help?' : 'How can we help?'}
      </Text>
      <Text className="mt-2 text-center text-sm leading-6 text-content-400">
        Start a conversation with our support team. Replies arrive right here, and you can leave this screen — we will
        keep the thread for you.
      </Text>

      <View className="mt-6 w-full flex-row flex-wrap gap-2">
        {TOPICS.map((topic) => (
          <Pressable
            key={topic.label}
            onPress={() => onTopic(topic.text)}
            accessibilityRole="button"
            className="grow basis-[45%] flex-row items-center gap-2.5 rounded-xl border border-surface-700 bg-surface-850 px-3 py-3 active:border-accent/50"
          >
            <Icon as={topic.icon} size={16} className="text-accent" />
            <Text className="text-sm font-medium text-content-200">{topic.label}</Text>
          </Pressable>
        ))}
      </View>

      <View className="mt-6 flex-row flex-wrap items-center justify-center gap-x-4 gap-y-1.5">
        <View className="flex-row items-center gap-1">
          <Icon as={ShieldCheck} size={14} className="text-up" />
          <Text className="text-[11px] text-content-400">Private to you and our team</Text>
        </View>
        <View className="flex-row items-center gap-1">
          <Icon as={Clock} size={14} className="text-content-400" />
          <Text className="text-[11px] text-content-400">Conversation history is kept</Text>
        </View>
      </View>
      <Text className="mt-4 text-center text-[11px] text-content-400">
        We will never ask for your password in this chat.
      </Text>
    </View>
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

/**
 * The thread as rows, oldest first.
 *
 * Messages from one side within a few minutes are grouped: one name, one
 * avatar, tighter spacing - how a conversation actually reads.
 */
function buildRows(messages: SupportMessage[], pending: PendingMessage[]): Row[] {
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
  return rows;
}

function ThreadRow({
  row,
  readUpTo,
  onRetry,
}: {
  row: Row;
  readUpTo: number;
  onRetry: (item: PendingMessage) => void;
}) {
  if (row.kind === 'day') {
    return (
      <View className="flex-row items-center gap-3 py-3">
        <View className="h-px flex-1 bg-surface-800" />
        <Text className="text-[10px] font-semibold uppercase tracking-wider text-content-400">{row.label}</Text>
        <View className="h-px flex-1 bg-surface-800" />
      </View>
    );
  }

  if (row.kind === 'pending') {
    return (
      <View className="flex-row justify-end pt-2">
        <View className="max-w-[78%] items-end">
          <View
            className={cn(
              'rounded-2xl rounded-br-md px-3.5 py-2',
              row.item.failed ? 'border border-down/40 bg-down/15' : 'bg-accent/70',
            )}
          >
            <Text className={cn('text-sm', row.item.failed ? 'text-content-100' : 'text-surface-950')}>
              {row.item.body}
            </Text>
          </View>
          <View className="mt-0.5 flex-row items-center justify-end gap-1">
            {row.item.failed ? (
              <Pressable
                onPress={() => onRetry(row.item)}
                accessibilityRole="button"
                hitSlop={6}
                className="flex-row items-center gap-1"
              >
                <Icon as={RotateCcw} size={12} className="text-down" />
                <Text className="text-[10px] font-semibold text-down">Not sent — tap to retry</Text>
              </Pressable>
            ) : (
              <>
                <Icon as={Clock} size={12} className="text-content-400" />
                <Text className="text-[10px] text-content-400">Sending</Text>
              </>
            )}
          </View>
        </View>
      </View>
    );
  }

  const { message, first, last } = row;

  if (message.sender === 'SYSTEM') {
    return (
      <View className="items-center py-2">
        <View className="rounded-full bg-surface-850 px-3 py-1">
          <Text className="text-center text-[11px] text-content-400">
            {message.body} · {timeLabel(message.createdAt)}
          </Text>
        </View>
      </View>
    );
  }

  const mine = message.sender === 'USER';
  const read = mine && new Date(message.createdAt).getTime() <= readUpTo;

  return (
    <View className={cn('flex-row gap-2 pt-1', mine ? 'justify-end' : 'justify-start', first && 'pt-3')}>
      {!mine ? (
        <View className="w-7">
          {last ? (
            <View className="h-7 w-7 items-center justify-center rounded-full bg-surface-700">
              <Text className="text-[11px] font-bold text-content-100">
                {message.senderName.slice(0, 1).toUpperCase()}
              </Text>
            </View>
          ) : null}
        </View>
      ) : null}

      <View className={cn('max-w-[78%]', mine && 'items-end')}>
        {!mine && first ? (
          <Text className="mb-0.5 ml-1 text-[11px] font-semibold text-content-300">
            {message.senderName} <Text className="text-[11px] font-normal text-content-400">· Support</Text>
          </Text>
        ) : null}
        <View
          className={cn(
            'rounded-2xl px-3.5 py-2',
            mine ? 'bg-accent' : 'bg-surface-800',
            mine && last && 'rounded-br-md',
            !mine && last && 'rounded-bl-md',
          )}
        >
          <Text selectable className={cn('text-sm leading-[22px]', mine ? 'text-surface-950' : 'text-content-100')}>
            {message.body}
          </Text>
        </View>
        {last ? (
          <View className={cn('mt-0.5 flex-row items-center gap-1 px-1', mine && 'justify-end')}>
            <Text className="text-[10px] text-content-400">{timeLabel(message.createdAt)}</Text>
            {mine ? (
              read ? (
                <View className="flex-row items-center gap-0.5" accessibilityLabel="Read by support">
                  <Icon as={CheckCheck} size={14} className="text-accent" />
                  <Text className="text-[10px] text-accent">Read</Text>
                </View>
              ) : (
                <View accessibilityLabel="Delivered">
                  <Icon as={Check} size={14} className="text-content-400" />
                </View>
              )
            ) : null}
          </View>
        ) : null}
      </View>
    </View>
  );
}

function TypingBubble({ name }: { name: string }) {
  return (
    <View className="flex-row items-end gap-2 pt-3" accessibilityLiveRegion="polite">
      <View className="h-7 w-7 items-center justify-center rounded-full bg-surface-700">
        <Text className="text-[11px] font-bold text-content-100">{name.slice(0, 1).toUpperCase()}</Text>
      </View>
      <View>
        <View className="flex-row items-center gap-1 rounded-2xl rounded-bl-md bg-surface-800 px-3.5 py-3">
          {[0, 1, 2].map((i) => (
            <BouncingDot key={i} delay={i * 140} />
          ))}
        </View>
        <Text className="ml-1 text-[10px] text-content-400">{name} is typing…</Text>
      </View>
    </View>
  );
}

/** One of the three typing dots: up and back, a little after its neighbour. */
function BouncingDot({ delay }: { delay: number }) {
  const lift = useSharedValue(0);
  useEffect(() => {
    lift.value = withDelay(
      delay,
      withRepeat(withSequence(withTiming(-4, { duration: 300 }), withTiming(0, { duration: 300 })), -1, false),
    );
  }, [delay, lift]);
  const style = useAnimatedStyle(() => ({ transform: [{ translateY: lift.value }] }));
  return <Animated.View className="h-1.5 w-1.5 rounded-full bg-content-300" style={style} />;
}
