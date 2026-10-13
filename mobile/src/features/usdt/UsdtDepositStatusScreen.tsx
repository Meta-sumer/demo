import { useEffect, useState } from 'react';
import { AppState, Pressable, View } from 'react-native';
import { router, useLocalSearchParams } from 'expo-router';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { AlertTriangle, CheckCircle2, ExternalLink, Loader2, RefreshCw } from 'lucide-react-native';
import { api } from '../../services/api';
import { ApiRequestError } from '../../services/http';
import { Screen, ScreenHeader } from '../../components/Screen';
import { Button, Icon, Panel, Text } from '../../components/ui';
import { cn } from '../../utils/cn';
import { formatDateTime } from '../../utils/format';
import { formatUsdt } from '../../utils/usdt';
import type { UsdtDepositView } from '../../types/api';
import {
  canStillPay,
  depositNeedsPolling,
  depositStatusExplanation,
  depositStatusTone,
  depositStatusWord,
  networkLabel,
  safeExternalUrl,
  usdtErrorMessage,
} from './model';
import { USDT_KEY, usdtKeys } from './queries';
import { useOpenCheckout } from './checkout';
import { DetailList, ErrorNote, Spin, StatusBadge } from './parts';

const POLL_MS = 4_000;
/** How long to keep asking on our own before handing the customer a button. */
const POLL_WINDOW_MS = 3 * 60_000;

/**
 * The status of a USDT deposit after its payment page: /wallet/usdt/success?deposit=<id>.
 *
 * The app shows it when the in-app browser hands back from the provider's
 * checkout (see `useOpenCheckout`); on the web the provider's redirect lands
 * on the same page.
 *
 * Arriving here proves nothing. Even a genuine return only means the customer
 * left the checkout - not that money arrived or was confirmed. So this screen
 * never decides anything itself: it asks the server about the deposit, and
 * only the server's `credited === true` (set once the provider's signed
 * callback or its own status check confirms the payment and the ledger has
 * been written) turns it into "Deposit completed".
 */
export function UsdtDepositStatusScreen() {
  const params = useLocalSearchParams<{ deposit?: string }>();
  const id = (typeof params.deposit === 'string' ? params.deposit.trim() : '') || null;
  const queryClient = useQueryClient();
  const [windowStart, setWindowStart] = useState(() => Date.now());
  const [timedOut, setTimedOut] = useState(false);

  useEffect(() => {
    setTimedOut(false);
    const timer = setTimeout(() => setTimedOut(true), Math.max(0, windowStart + POLL_WINDOW_MS - Date.now()));
    return () => clearTimeout(timer);
  }, [windowStart]);

  const query = useQuery({
    queryKey: usdtKeys.deposit(id ?? ''),
    queryFn: () => api.usdt.deposit(id!),
    enabled: !!id,
    refetchInterval: (q) => {
      const deposit = q.state.data;
      if (timedOut || (deposit && !depositNeedsPolling(deposit))) return false;
      if (!deposit && isNotFound(q.state.error)) return false;
      return POLL_MS;
    },
  });
  const deposit = query.data;
  const refetch = query.refetch;

  // On Android the checkout opens over this screen and the customer comes
  // back to it later: ask again then, and give the automatic checks a fresh
  // window from that moment, as the web's redirect would.
  useEffect(() => {
    if (!id) return;
    const subscription = AppState.addEventListener('change', (state) => {
      if (state !== 'active') return;
      setWindowStart(Date.now());
      void refetch();
    });
    return () => subscription.remove();
  }, [id, refetch]);

  // When it lands, the wallet screen and its lists should already know.
  const credited = !!deposit && isCompleted(deposit);
  useEffect(() => {
    if (credited) void queryClient.invalidateQueries({ queryKey: USDT_KEY, refetchType: 'none' });
  }, [credited, queryClient]);

  const refresh = useMutation({
    mutationFn: () => api.usdt.refreshDeposit(id!),
    onSuccess: (fresh) => {
      queryClient.setQueryData(usdtKeys.deposit(fresh.id), fresh);
      // Asked by hand: give the automatic checks another window.
      setWindowStart(Date.now());
    },
  });

  return (
    <View className="flex-1 bg-surface-950">
      <ScreenHeader title="USDT deposit" fallbackHref="/wallet/usdt" />
      <Screen bottomInset>
        <Panel className="p-5">
          {!id ? (
            <Outcome
              tone="neutral"
              title="No deposit to check"
              text={
                'This screen shows the status of a USDT deposit after you pay for it. ' +
                'Open your USDT wallet to see all deposits.'
              }
            />
          ) : query.isLoading ? (
            <Outcome tone="pending" title="Verifying your payment…" text="Checking the status of your deposit." />
          ) : !deposit ? (
            <View className="gap-4">
              <Outcome
                tone="neutral"
                title={isNotFound(query.error) ? 'We could not find this deposit' : 'Could not check this deposit'}
                text={
                  isNotFound(query.error)
                    ? 'The link may be incomplete. Your USDT wallet lists every deposit you have made.'
                    : usdtErrorMessage(query.error)
                }
              />
              {!isNotFound(query.error) ? (
                <Button variant="ghost" onPress={() => void query.refetch()}>
                  Try again
                </Button>
              ) : null}
            </View>
          ) : (
            <DepositOutcome
              deposit={deposit}
              timedOut={timedOut}
              refreshing={refresh.isPending}
              refreshError={refresh.isError ? usdtErrorMessage(refresh.error) : null}
              onRefresh={() => refresh.mutate()}
              onCheckoutReturn={() => setWindowStart(Date.now())}
            />
          )}

          <Button
            variant={credited ? 'primary' : 'ghost'}
            className="mt-4"
            // Back to the wallet screen this was opened from, or onto it when
            // the screen was reached some other way.
            onPress={() => router.dismissTo('/wallet/usdt')}
          >
            Go to USDT wallet
          </Button>
        </Panel>
      </Screen>
    </View>
  );
}

/** Completed means the server says credited, on a paid or overpaid deposit - nothing less. */
function isCompleted(deposit: UsdtDepositView): boolean {
  return deposit.credited === true && (deposit.status === 'paid' || deposit.status === 'overpaid');
}

function isNotFound(error: unknown): boolean {
  return error instanceof ApiRequestError && (error.status === 404 || error.code === 'NOT_FOUND');
}

function DepositOutcome({
  deposit,
  timedOut,
  refreshing,
  refreshError,
  onRefresh,
  onCheckoutReturn,
}: {
  deposit: UsdtDepositView;
  timedOut: boolean;
  refreshing: boolean;
  refreshError: string | null;
  onRefresh: () => void;
  onCheckoutReturn: () => void;
}) {
  const label = networkLabel(deposit.network);
  const paymentUrl = canStillPay(deposit.status) ? safeExternalUrl(deposit.paymentUrl) : null;
  const openCheckout = useOpenCheckout();

  if (isCompleted(deposit)) {
    return (
      <View className="gap-4">
        <Outcome tone="done" title="Deposit completed" text={depositStatusExplanation(deposit, label)} />
        <Text className="text-center text-3xl font-bold text-up tnum">
          {formatUsdt(deposit.amountCredited, { signed: true })}
        </Text>
        <DetailList
          rows={[
            { label: 'Network', value: label },
            { label: 'Status', value: depositStatusWord(deposit.status) },
            deposit.creditedAt ? { label: 'Credited at', value: formatDateTime(deposit.creditedAt) } : null,
            { label: 'Reference', value: deposit.reference, mono: true },
          ]}
        />
      </View>
    );
  }

  const waiting = depositNeedsPolling(deposit);
  return (
    <View className="gap-4">
      <Outcome
        tone={waiting ? 'pending' : 'stopped'}
        title={waiting ? 'Verifying your payment…' : stoppedTitle(deposit.status)}
        text={depositStatusExplanation(deposit, label)}
      />
      <View className="items-center">
        <StatusBadge tone={depositStatusTone(deposit.status)}>{depositStatusWord(deposit.status)}</StatusBadge>
      </View>
      <DetailList
        rows={[
          { label: 'Amount requested', value: formatUsdt(deposit.amountExpected), strong: true },
          deposit.amountReceived > 0
            ? { label: waiting ? 'Received so far' : 'Received', value: formatUsdt(deposit.amountReceived) }
            : null,
          { label: 'Network', value: label },
          { label: 'Reference', value: deposit.reference, mono: true },
        ]}
      />

      {waiting ? (
        <Text className="text-center text-xs text-content-400">
          Confirmation can take a few minutes. You can leave this screen - your USDT wallet updates by itself.
        </Text>
      ) : null}

      <ErrorNote message={refreshError} />

      {waiting && timedOut ? (
        <Button variant="ghost" icon={RefreshCw} loading={refreshing} disabled={refreshing} onPress={onRefresh}>
          Refresh status
        </Button>
      ) : null}

      {paymentUrl ? (
        <Button
          variant="ghost"
          iconRight={ExternalLink}
          onPress={() =>
            void openCheckout(deposit.id, paymentUrl, { showStatus: false, onReturn: onCheckoutReturn })
          }
        >
          Open payment page
        </Button>
      ) : null}

      {deposit.status === 'underpaid' || deposit.status === 'failed' || deposit.status === 'expired' ? (
        <Pressable
          accessibilityRole="link"
          onPress={() => router.push('/support')}
          className="self-center py-1 active:opacity-70"
        >
          <Text className="text-center text-xs font-semibold text-accent">Contact support</Text>
        </Pressable>
      ) : null}
    </View>
  );
}

function stoppedTitle(status: string): string {
  switch (status) {
    case 'underpaid':
      return 'Payment was less than requested';
    case 'expired':
      return 'Deposit expired';
    case 'cancelled':
      return 'Deposit cancelled';
    case 'failed':
      return 'Deposit failed';
    default:
      return depositStatusWord(status);
  }
}

function Outcome({
  tone,
  title,
  text,
}: {
  tone: 'pending' | 'done' | 'stopped' | 'neutral';
  title: string;
  text: string;
}) {
  const glyph = tone === 'done' ? CheckCircle2 : tone === 'pending' ? Loader2 : AlertTriangle;
  const icon = (
    <Icon
      as={glyph}
      size={24}
      className={
        tone === 'done'
          ? 'text-up'
          : tone === 'pending'
            ? 'text-accent'
            : tone === 'stopped'
              ? 'text-warn'
              : 'text-content-300'
      }
    />
  );
  return (
    <View className="items-center gap-2" accessibilityLiveRegion="polite">
      <View
        className={cn(
          'h-12 w-12 items-center justify-center rounded-full',
          tone === 'done'
            ? 'bg-up/15'
            : tone === 'pending'
              ? 'bg-accent/15'
              : tone === 'stopped'
                ? 'bg-warn/15'
                : 'bg-surface-800',
        )}
      >
        {tone === 'pending' ? <Spin>{icon}</Spin> : icon}
      </View>
      <Text accessibilityRole="header" className="text-center text-lg font-bold text-content-100">
        {title}
      </Text>
      {text ? <Text className="text-center text-sm text-content-300">{text}</Text> : null}
    </View>
  );
}
