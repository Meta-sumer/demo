import { useState, type FormEvent } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { ArrowRight, CheckCircle2 } from 'lucide-react';
import { api } from '../../services/api';
import { Spinner } from '../../components/ui';
import { formatUsdt, parseUsdt } from '../../utils/usdt';
import type { UsdtTransferResult, UsdtWalletView, Wallet } from '../../types/api';
import { amountProblem, createAttemptKey, usdtErrorMessage } from './model';
import { USDT_KEY, useSendOnce } from './queries';
import { AmountInput, DetailList, ErrorNote } from './parts';

/**
 * Moves USDT from the wallet to the trading side, where it can be staked.
 *
 * Internal and immediate - nothing goes on chain - but still money, so it
 * is confirmed before it is sent and carries an idempotency key like any
 * other movement.
 */
export function TransferFlow({
  wallet,
  trading,
  onDone,
}: {
  wallet: UsdtWalletView | null;
  trading: Wallet | null;
  onDone: () => void;
}) {
  const queryClient = useQueryClient();
  const [amountText, setAmountText] = useState('');
  const [reviewing, setReviewing] = useState(false);
  const [attempt] = useState(() => createAttemptKey());
  const [result, setResult] = useState<UsdtTransferResult | null>(null);

  const available = wallet?.availableBalance ?? 0;
  const amount = parseUsdt(amountText);
  const problem =
    amountProblem(amountText, amount) ??
    (amount !== null && amount > available
      ? 'That is more than your available balance of ' + formatUsdt(available, { trim: true }) + '.'
      : null);
  const ready = amount !== null && amount > 0 && !problem;

  const mutation = useMutation({
    mutationFn: (body: { amount: number }) => api.usdt.transferToTrading(body, attempt.for(body)),
    onSuccess: (transfer) => {
      attempt.reset();
      void queryClient.invalidateQueries({ queryKey: USDT_KEY });
      // The trading side's balances live under `wallet`.
      void queryClient.invalidateQueries({ queryKey: ['wallet'] });
      setResult(transfer);
    },
  });
  const send = useSendOnce(mutation.mutate);

  if (result) {
    return (
      <div className="space-y-4">
        <div className="flex items-center gap-3">
          <span className="grid h-9 w-9 shrink-0 place-items-center rounded-full bg-up/15 text-up">
            <CheckCircle2 className="h-5 w-5" aria-hidden />
          </span>
          <div className="min-w-0">
            <h3 className="text-sm font-bold text-content-100">Transfer complete</h3>
            <p className="text-xs text-content-400">
              {formatUsdt(result.transfer.amount)} moved to Trading
            </p>
          </div>
        </div>
        <DetailList
          rows={[
            { label: 'Moved', value: formatUsdt(result.transfer.amount), strong: true },
            { label: 'USDT wallet, available', value: formatUsdt(result.wallet.availableBalance) },
            {
              label: 'Trading, USDT balance',
              value: result.trading ? formatUsdt(result.trading.balance) : '--',
            },
          ]}
        />
        <button type="button" className="btn-primary w-full" onClick={onDone}>
          Done
        </button>
      </div>
    );
  }

  if (reviewing && amount !== null) {
    return (
      <div className="space-y-4">
        <h3 className="text-sm font-bold text-content-100">Check your transfer</h3>
        <DetailList
          rows={[
            { label: 'Amount', value: formatUsdt(amount), strong: true },
            { label: 'From', value: 'USDT wallet' },
            { label: 'To', value: 'Trading' },
            { label: 'USDT wallet after', value: formatUsdt(Math.max(0, available - amount)) },
          ]}
        />
        <ErrorNote message={mutation.isError ? usdtErrorMessage(mutation.error) : null} />
        <div className="grid grid-cols-[auto_1fr] gap-2">
          <button
            type="button"
            className="btn-ghost"
            disabled={mutation.isPending}
            onClick={() => {
              mutation.reset();
              setReviewing(false);
            }}
          >
            Edit
          </button>
          <button
            type="button"
            className="btn-primary"
            disabled={mutation.isPending}
            onClick={() => {
              send({ amount });
            }}
          >
            {mutation.isPending ? <Spinner /> : null}
            Confirm transfer
          </button>
        </div>
      </div>
    );
  }

  function onSubmit(event: FormEvent) {
    event.preventDefault();
    if (ready) setReviewing(true);
  }

  return (
    <form onSubmit={onSubmit} className="space-y-4" noValidate>
      <div className="grid grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)] items-center gap-2">
        <div className="min-w-0 rounded-lg border border-surface-700 bg-surface-850 px-3 py-2">
          <div className="text-[11px] uppercase tracking-wider text-content-400">From</div>
          <div className="text-sm font-semibold text-content-100">USDT wallet</div>
          <div className="truncate text-[11px] text-content-400 tnum">{formatUsdt(available, { trim: true })}</div>
        </div>
        <ArrowRight className="h-4 w-4 text-content-400" aria-hidden />
        <div className="min-w-0 rounded-lg border border-surface-700 bg-surface-850 px-3 py-2">
          <div className="text-[11px] uppercase tracking-wider text-content-400">To</div>
          <div className="text-sm font-semibold text-content-100">Trading</div>
          <div className="truncate text-[11px] text-content-400 tnum">
            {trading ? formatUsdt(trading.balance, { trim: true }) : 'USDT balance'}
          </div>
        </div>
      </div>

      <AmountInput
        id="usdt-transfer-amount"
        value={amountText}
        onChange={(text) => {
          setAmountText(text);
          mutation.reset();
        }}
        max={available}
        error={problem}
        hint={
          <>
            Available: <span className="font-semibold text-content-200 tnum">{formatUsdt(available)}</span>
          </>
        }
      />

      <button type="submit" className="btn-primary w-full" disabled={!ready}>
        Review transfer
      </button>
      <p className="text-center text-[11px] leading-relaxed text-content-400">
        The transfer happens straight away. Your INR Live and Demo balances are not affected.
      </p>
    </form>
  );
}
