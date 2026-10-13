import { useEffect, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import clsx from 'clsx';
import { AlertTriangle, ArrowLeft, CheckCircle2, ExternalLink, Loader2, RefreshCw } from 'lucide-react';
import { api } from '../services/api';
import { ApiRequestError } from '../services/http';
import { Panel, Spinner } from '../components/ui';
import { formatDateTime } from '../utils/format';
import { formatUsdt } from '../utils/usdt';
import type { UsdtDepositView } from '../types/api';
import {
  canStillPay,
  depositNeedsPolling,
  depositStatusExplanation,
  depositStatusTone,
  depositStatusWord,
  networkLabel,
  safeExternalUrl,
  usdtErrorMessage,
} from '../features/usdt/model';
import { USDT_KEY, usdtKeys } from '../features/usdt/queries';
import { DetailList, ErrorNote, StatusBadge } from '../features/usdt/parts';

const POLL_MS = 4_000;
/** How long to keep asking on our own before handing the customer a button. */
const POLL_WINDOW_MS = 3 * 60_000;

/**
 * Where the payment provider sends the customer back after checkout:
 * /wallet/usdt/success?deposit=<id>.
 *
 * Arriving here proves nothing. The address is public and anyone can open it
 * with any id, and even a genuine redirect only means the customer left the
 * checkout - not that money arrived or was confirmed. So this page never
 * decides anything itself: it asks the server about the deposit, and only
 * the server's `credited === true` (set once the provider's signed callback
 * or its own status check confirms the payment and the ledger has been
 * written) turns it into "Deposit completed".
 */
export function UsdtDepositStatusPage() {
  const [params] = useSearchParams();
  const id = params.get('deposit')?.trim() || null;
  const queryClient = useQueryClient();
  const [windowStart, setWindowStart] = useState(() => Date.now());
  const [timedOut, setTimedOut] = useState(false);

  useEffect(() => {
    setTimedOut(false);
    const timer = window.setTimeout(() => setTimedOut(true), Math.max(0, windowStart + POLL_WINDOW_MS - Date.now()));
    return () => window.clearTimeout(timer);
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

  // When it lands, the wallet page and its lists should already know.
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
    <div className="mx-auto max-w-md space-y-3">
      <Link to="/wallet/usdt" className="inline-flex items-center gap-1.5 text-sm text-content-400 hover:text-content-100">
        <ArrowLeft className="h-4 w-4" />
        USDT wallet
      </Link>

      <Panel className="p-5">
        {!id ? (
          <Outcome
            tone="neutral"
            title="No deposit to check"
            text={
              'This page shows the status of a USDT deposit after you pay for it. ' +
              'Open your USDT wallet to see all deposits.'
            }
          />
        ) : query.isLoading ? (
          <Outcome tone="pending" title="Verifying your payment…" text="Checking the status of your deposit." />
        ) : !deposit ? (
          <div className="space-y-4">
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
              <button type="button" className="btn-ghost w-full" onClick={() => void query.refetch()}>
                Try again
              </button>
            ) : null}
          </div>
        ) : (
          <DepositOutcome
            deposit={deposit}
            timedOut={timedOut}
            refreshing={refresh.isPending}
            refreshError={refresh.isError ? usdtErrorMessage(refresh.error) : null}
            onRefresh={() => refresh.mutate()}
          />
        )}

        <Link to="/wallet/usdt" className={clsx('mt-4 w-full', credited ? 'btn-primary' : 'btn-ghost')}>
          Go to USDT wallet
        </Link>
      </Panel>
    </div>
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
}: {
  deposit: UsdtDepositView;
  timedOut: boolean;
  refreshing: boolean;
  refreshError: string | null;
  onRefresh: () => void;
}) {
  const label = networkLabel(deposit.network);
  const paymentUrl = canStillPay(deposit.status) ? safeExternalUrl(deposit.paymentUrl) : null;

  if (isCompleted(deposit)) {
    return (
      <div className="space-y-4">
        <Outcome tone="done" title="Deposit completed" text={depositStatusExplanation(deposit, label)} />
        <p className="text-center text-3xl font-bold text-up tnum">{formatUsdt(deposit.amountCredited, { signed: true })}</p>
        <DetailList
          rows={[
            { label: 'Network', value: label },
            { label: 'Status', value: depositStatusWord(deposit.status) },
            deposit.creditedAt ? { label: 'Credited at', value: formatDateTime(deposit.creditedAt) } : null,
            { label: 'Reference', value: deposit.reference, mono: true },
          ]}
        />
      </div>
    );
  }

  const waiting = depositNeedsPolling(deposit);
  return (
    <div className="space-y-4">
      <Outcome
        tone={waiting ? 'pending' : 'stopped'}
        title={waiting ? 'Verifying your payment…' : stoppedTitle(deposit.status)}
        text={depositStatusExplanation(deposit, label)}
      />
      <div className="flex justify-center">
        <StatusBadge tone={depositStatusTone(deposit.status)}>{depositStatusWord(deposit.status)}</StatusBadge>
      </div>
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
        <p className="text-center text-xs text-content-400">
          Confirmation can take a few minutes. You can leave this page - your USDT wallet updates by itself.
        </p>
      ) : null}

      <ErrorNote message={refreshError} />

      {waiting && timedOut ? (
        <button type="button" className="btn-ghost w-full" onClick={onRefresh} disabled={refreshing}>
          {refreshing ? <Spinner /> : <RefreshCw className="h-4 w-4" />}
          Refresh status
        </button>
      ) : null}

      {paymentUrl ? (
        <a href={paymentUrl} className="btn-ghost w-full">
          Open payment page
          <ExternalLink className="h-4 w-4" />
        </a>
      ) : null}

      {deposit.status === 'underpaid' || deposit.status === 'failed' || deposit.status === 'expired' ? (
        <Link to="/account/support" className="block text-center text-xs font-semibold text-accent hover:underline">
          Contact support
        </Link>
      ) : null}
    </div>
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
  const Icon = tone === 'done' ? CheckCircle2 : tone === 'pending' ? Loader2 : AlertTriangle;
  return (
    <div className="flex flex-col items-center gap-2 text-center" aria-live="polite">
      <span
        className={clsx(
          'grid h-12 w-12 place-items-center rounded-full',
          tone === 'done'
            ? 'bg-up/15 text-up'
            : tone === 'pending'
              ? 'bg-accent/15 text-accent'
              : tone === 'stopped'
                ? 'bg-warn/15 text-warn'
                : 'bg-surface-800 text-content-300',
        )}
      >
        <Icon className={clsx('h-6 w-6', tone === 'pending' && 'animate-spin')} aria-hidden />
      </span>
      <h1 className="text-lg font-bold text-content-100">{title}</h1>
      {text ? <p className="text-sm text-content-300">{text}</p> : null}
    </div>
  );
}
