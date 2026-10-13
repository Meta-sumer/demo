import { useState, type FormEvent } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import clsx from 'clsx';
import { CheckCircle2, Clock3, ExternalLink, Loader2, XCircle } from 'lucide-react';
import { api } from '../../services/api';
import { Spinner } from '../../components/ui';
import { InfoTip } from '../../components/InfoTip';
import { useDebouncedValue } from '../../hooks/useDebouncedValue';
import { formatUsdt, parseUsdt } from '../../utils/usdt';
import type { UsdtNetworkView, UsdtWalletView, UsdtWithdrawalQuote, UsdtWithdrawalView } from '../../types/api';
import {
  addressHint,
  amountProblem,
  createAttemptKey,
  explorerTxUrl,
  isWithdrawalFinal,
  isWithdrawalReturned,
  networkLabel,
  usdtErrorMessage,
  withdrawalLimitProblem,
  withdrawalStatusExplanation,
  withdrawalStatusTone,
  withdrawalStatusWord,
} from './model';
import { USDT_KEY, usdtKeys, useSendOnce, useUsdtWithdrawal } from './queries';
import { AmountInput, CopyValue, DetailList, ErrorNote, NetworkPicker, NetworkWarning, StatusBadge } from './parts';

const QUOTE_DEBOUNCE_MS = 400;

/**
 * USDT withdrawal: network, address, amount, a live quote, a confirmation
 * that repeats all of it, then the request and its progress.
 *
 * The amount is reserved the moment the server accepts the request. The fee
 * on the quote is the provider's estimate; the withdrawal record carries the
 * final one, and once it exists that is what is shown.
 */
export function WithdrawFlow({
  networks,
  wallet,
  onDone,
}: {
  networks: UsdtNetworkView[];
  wallet: UsdtWalletView | null;
  onDone: () => void;
}) {
  const queryClient = useQueryClient();
  const open = networks.filter((network) => network.withdrawEnabled);
  const [code, setCode] = useState<string>(open[0]?.code ?? '');
  const [address, setAddress] = useState('');
  const [amountText, setAmountText] = useState('');
  const [step, setStep] = useState<'form' | 'review'>('form');
  const [attempt] = useState(() => createAttemptKey());
  const [result, setResult] = useState<UsdtWithdrawalView | null>(null);

  const network = open.find((n) => n.code === code) ?? null;
  const amount = parseUsdt(amountText);
  const settledAmount = useDebouncedValue(amount, QUOTE_DEBOUNCE_MS);
  const available = wallet?.availableBalance ?? null;

  const quoteQuery = useQuery({
    queryKey: usdtKeys.quote(code, settledAmount ?? 0),
    queryFn: () => api.usdt.withdrawalQuote({ network: code, amount: settledAmount! }),
    enabled: !!network && settledAmount !== null && settledAmount > 0 && step === 'form',
    placeholderData: (previous) => previous,
    staleTime: 15_000,
    retry: false,
  });
  // A quote counts only for exactly what is on screen now: not while the
  // amount is still settling, and not the previous amount's placeholder.
  const quote: UsdtWithdrawalQuote | null =
    quoteQuery.data && !quoteQuery.isPlaceholderData && settledAmount === amount && amount !== null
      ? quoteQuery.data
      : null;

  const mutation = useMutation({
    mutationFn: (body: { network: string; address: string; amount: number }) =>
      api.usdt.withdraw(body, attempt.for(body)),
    onSuccess: (withdrawal) => {
      attempt.reset();
      queryClient.setQueryData(usdtKeys.withdrawal(withdrawal.id), withdrawal);
      void queryClient.invalidateQueries({ queryKey: USDT_KEY });
      setResult(withdrawal);
    },
  });
  const send = useSendOnce(mutation.mutate);

  if (result) return <WithdrawalProgress initial={result} networks={networks} onDone={onDone} />;

  if (open.length === 0) {
    return (
      <p className="rounded-lg border border-surface-700 bg-surface-850 px-4 py-3 text-sm text-content-300">
        USDT withdrawals are paused on every network right now. Please try again later.
      </p>
    );
  }

  const label = network?.label ?? '';
  const hint = network ? addressHint(network.code, address) : null;
  const typed = amountProblem(amountText, amount);
  const limit = amount !== null && amount > 0 ? withdrawalLimitProblem(amount, available, quote) : null;
  const quoteError = quoteQuery.isError && settledAmount === amount ? usdtErrorMessage(quoteQuery.error) : null;
  const ready =
    !!network && address.trim().length > 0 && amount !== null && amount > 0 && !typed && !limit && !!quote;

  function edit() {
    mutation.reset();
  }

  if (step === 'review' && network && amount !== null && quote) {
    const body = { network: network.code, address: address.trim(), amount };
    return (
      <div className="space-y-4">
        <h3 className="text-sm font-bold text-content-100">Check your withdrawal</h3>
        <DetailList
          rows={[
            { label: 'Network', value: label + (network.name ? ' · ' + network.name : '') },
            { label: 'To address', value: body.address, mono: true },
            { label: 'Amount', value: formatUsdt(quote.amount) },
            { label: quote.feeIsEstimate ? 'Fee (estimate)' : 'Fee', value: formatUsdt(quote.feeAmount) },
            { label: 'You receive', value: formatUsdt(quote.netAmount), strong: true },
            available !== null
              ? { label: 'Available after', value: formatUsdt(Math.max(0, available - quote.amount)) }
              : null,
          ]}
        />
        {quote.needsReview ? (
          <p className="text-xs text-content-300">
            This withdrawal is checked by our team before it is sent. The amount stays reserved until then.
          </p>
        ) : null}
        <NetworkWarning>
          Withdrawals cannot be reversed once sent. Make sure the address is a USDT address on the {label} network.
        </NetworkWarning>

        <ErrorNote message={mutation.isError ? usdtErrorMessage(mutation.error) : null} />

        <div className="grid grid-cols-[auto_1fr] gap-2">
          <button
            type="button"
            className="btn-ghost"
            disabled={mutation.isPending}
            onClick={() => {
              edit();
              setStep('form');
            }}
          >
            Edit
          </button>
          <button
            type="button"
            className="btn-primary"
            disabled={mutation.isPending}
            onClick={() => {
              send(body);
            }}
          >
            {mutation.isPending ? <Spinner /> : null}
            Confirm withdrawal
          </button>
        </div>
      </div>
    );
  }

  function onSubmit(event: FormEvent) {
    event.preventDefault();
    if (ready) setStep('review');
  }

  return (
    <form onSubmit={onSubmit} className="space-y-4" noValidate>
      <NetworkPicker
        purpose="withdraw"
        networks={open}
        value={code}
        onChange={(next) => {
          setCode(next);
          edit();
        }}
      />

      <div>
        <label className="label" htmlFor="usdt-withdraw-address">
          Destination address
        </label>
        <input
          id="usdt-withdraw-address"
          className={clsx('field font-mono text-[13px]', hint && 'border-warn')}
          autoComplete="off"
          autoCapitalize="off"
          autoCorrect="off"
          spellCheck={false}
          placeholder={network?.addressExample || (label === 'BSC' ? '0x…' : 'T…')}
          value={address}
          aria-describedby="usdt-withdraw-address-hint"
          onChange={(event) => {
            setAddress(event.target.value);
            edit();
          }}
        />
        <p
          id="usdt-withdraw-address-hint"
          className={clsx('mt-1 text-[11px]', hint ? 'text-warn' : 'text-content-400')}
        >
          {hint ?? 'A USDT address on the ' + label + ' network. Paste it rather than typing it.'}
        </p>
      </div>

      <AmountInput
        id="usdt-withdraw-amount"
        value={amountText}
        onChange={(text) => {
          setAmountText(text);
          edit();
        }}
        max={available ?? undefined}
        error={typed ?? limit ?? quoteError}
        hint={
          available !== null ? (
            <>
              Available:{' '}
              <span className="font-semibold text-content-200 tnum">{formatUsdt(available)}</span>
            </>
          ) : undefined
        }
      />

      {amount !== null && amount > 0 && !typed && !quoteError ? (
        // The last quote stays up, dimmed, while the next one is fetched.
        <QuotePanel quote={quote ?? quoteQuery.data ?? null} loading={!quote} />
      ) : null}

      <button type="submit" className="btn-primary w-full" disabled={!ready}>
        Review withdrawal
      </button>
    </form>
  );
}

/** Amount, fee and what arrives, with the limits that apply - updated as the amount changes. */
function QuotePanel({ quote, loading }: { quote: UsdtWithdrawalQuote | null; loading: boolean }) {
  if (!quote) {
    return (
      <div className="flex items-center gap-2 rounded-lg border border-surface-700 bg-surface-850 px-3.5 py-3 text-xs text-content-400">
        {loading ? <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden /> : null}
        {loading ? 'Calculating the fee…' : 'Enter an amount to see the fee.'}
      </div>
    );
  }
  const limits = [
    quote.minWithdrawal > 0 ? 'Min ' + formatUsdt(quote.minWithdrawal, { trim: true }) : null,
    quote.maxWithdrawal > 0 ? 'Max ' + formatUsdt(quote.maxWithdrawal, { trim: true }) : null,
    quote.dailyCap > 0
      ? 'Daily limit ' +
        formatUsdt(quote.dailyCap, { trim: true }) +
        ' (' +
        formatUsdt(quote.dailyUsed, { trim: true, unit: false }) +
        ' used)'
      : null,
  ].filter(Boolean);

  return (
    <div
      className={clsx('space-y-2 transition-opacity', loading && 'opacity-60')}
      aria-live="polite"
      aria-busy={loading}
    >
      <DetailList
        rows={[
          { label: 'Amount', value: formatUsdt(quote.amount) },
          {
            label: (
              <span className="inline-flex items-center gap-0.5">
                {quote.feeIsEstimate ? 'Fee (estimate)' : 'Fee'}
                {quote.feeIsEstimate ? (
                  <InfoTip label="the fee estimate">
                    The network fee can change a little until the withdrawal is sent. The final fee is shown on the
                    withdrawal once it is created.
                  </InfoTip>
                ) : null}
              </span>
            ),
            value: formatUsdt(quote.feeAmount),
          },
          { label: 'You receive', value: formatUsdt(quote.netAmount), strong: true },
        ]}
      />
      {limits.length > 0 ? <p className="text-[11px] text-content-400 tnum">{limits.join(' · ')}</p> : null}
      {quote.needsReview ? (
        <p className="text-[11px] text-content-300">
          {quote.reviewedAbove > 0
            ? 'Withdrawals above ' + formatUsdt(quote.reviewedAbove, { trim: true }) + ' are checked by our team first.'
            : 'This withdrawal is checked by our team first.'}
        </p>
      ) : null}
    </div>
  );
}

/**
 * The withdrawal after it was accepted, kept current until it is completed,
 * failed or rejected. Amounts come from the record - the final fee, not the
 * estimate that was quoted.
 */
export function WithdrawalProgress({
  initial,
  networks,
  onDone,
}: {
  initial: UsdtWithdrawalView;
  networks: UsdtNetworkView[];
  onDone?: () => void;
}) {
  const query = useUsdtWithdrawal(initial.id, initial);
  const withdrawal = query.data ?? initial;
  return (
    <div className="space-y-4">
      <WithdrawalSummary withdrawal={withdrawal} networks={networks} />
      {onDone ? (
        <button type="button" className="btn-ghost w-full" onClick={onDone}>
          Done
        </button>
      ) : null}
    </div>
  );
}

export function WithdrawalSummary({
  withdrawal,
  networks,
}: {
  withdrawal: UsdtWithdrawalView;
  networks: UsdtNetworkView[];
}) {
  const label = networkLabel(withdrawal.network, networks);
  const final = isWithdrawalFinal(withdrawal.status);
  const returned = isWithdrawalReturned(withdrawal.status);
  const reason = withdrawal.failureReason || withdrawal.reviewNote;
  const explorer = withdrawal.txHash ? explorerTxUrl(withdrawal.network, withdrawal.txHash) : null;
  // A review can take hours, so it waits on a clock; a payout on its way spins.
  const reviewing = withdrawal.status === 'pending_review';
  const Icon = withdrawal.status === 'completed' ? CheckCircle2 : returned ? XCircle : reviewing ? Clock3 : Loader2;

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-3">
        <span
          className={clsx(
            'grid h-9 w-9 shrink-0 place-items-center rounded-full',
            withdrawal.status === 'completed'
              ? 'bg-up/15 text-up'
              : returned
                ? 'bg-down/15 text-down'
                : 'bg-warn/15 text-warn',
          )}
        >
          <Icon className={clsx('h-5 w-5', !final && !reviewing && 'animate-spin')} aria-hidden />
        </span>
        <div className="min-w-0 flex-1">
          <h3 className="text-sm font-bold text-content-100">
            {withdrawal.status === 'completed'
              ? 'Withdrawal completed'
              : returned
                ? withdrawal.status === 'rejected'
                  ? 'Withdrawal rejected'
                  : 'Withdrawal failed'
                : 'Withdrawal requested'}
          </h3>
          <p className="text-xs text-content-400">Reference {withdrawal.reference}</p>
        </div>
        <StatusBadge tone={withdrawalStatusTone(withdrawal.status)}>
          {withdrawalStatusWord(withdrawal.status)}
        </StatusBadge>
      </div>

      <p className="text-sm text-content-300">{withdrawalStatusExplanation(withdrawal, label)}</p>

      {returned ? (
        <div className="rounded-lg border border-down/30 bg-down/10 px-3 py-2.5 text-sm">
          {reason ? <p className="text-content-100">{reason}</p> : null}
          <p className={clsx('text-content-300', reason && 'mt-1 text-xs')}>
            The funds were returned to your wallet.
          </p>
        </div>
      ) : null}

      <DetailList
        rows={[
          { label: 'Network', value: label },
          { label: 'To address', value: withdrawal.address, mono: true },
          { label: 'Amount', value: formatUsdt(withdrawal.amount) },
          { label: 'Fee', value: formatUsdt(withdrawal.feeAmount) },
          {
            label: returned ? 'Would have received' : 'You receive',
            value: formatUsdt(withdrawal.netAmount),
            strong: true,
          },
        ]}
      />

      {withdrawal.txHash ? (
        <div className="space-y-1.5">
          <CopyValue label="TX hash" value={withdrawal.txHash} />
          {explorer ? (
            <a
              href={explorer}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-1 text-xs font-semibold text-accent hover:underline"
            >
              View on the {label} explorer
              <ExternalLink className="h-3.5 w-3.5" />
            </a>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
