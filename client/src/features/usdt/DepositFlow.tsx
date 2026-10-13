import { useState, type FormEvent } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { ExternalLink } from 'lucide-react';
import { api } from '../../services/api';
import { Spinner } from '../../components/ui';
import { formatDateTime } from '../../utils/format';
import { formatUsdt, parseUsdt } from '../../utils/usdt';
import type { UsdtDepositView, UsdtNetworkView } from '../../types/api';
import {
  amountProblem,
  canStillPay,
  createAttemptKey,
  depositStatusExplanation,
  depositStatusTone,
  depositStatusWord,
  networkLabel,
  safeExternalUrl,
  usdtErrorMessage,
} from './model';
import { USDT_KEY, usdtKeys, useSendOnce, useUsdtDeposit } from './queries';
import {
  AmountInput,
  CopyValue,
  DetailList,
  ErrorNote,
  NetworkPicker,
  NetworkWarning,
  StatusBadge,
  UsdtMark,
} from './parts';

/**
 * USDT deposit: network, amount, then the provider's checkout.
 *
 * Continue creates the deposit on the server, which opens an invoice with the
 * payment provider and hands back its checkout page and the address to pay.
 * Nothing is credited here: the wallet moves only when the server has been
 * told by the provider that the money arrived. The API key the provider
 * issues stays on the server - the browser only ever sees the checkout link.
 */
export function DepositFlow({ networks, onDone }: { networks: UsdtNetworkView[]; onDone: () => void }) {
  const queryClient = useQueryClient();
  const open = networks.filter((network) => network.depositEnabled);
  const [code, setCode] = useState<string>(open[0]?.code ?? '');
  const [amountText, setAmountText] = useState('');
  const [attempt] = useState(() => createAttemptKey());
  const [created, setCreated] = useState<UsdtDepositView | null>(null);

  const network = open.find((n) => n.code === code) ?? null;
  const amount = parseUsdt(amountText);

  const mutation = useMutation({
    // Same body, same key: a double click or a retry after a timeout is one
    // deposit, not two invoices.
    mutationFn: (body: { network: string; amount: number }) => api.usdt.createDeposit(body, attempt.for(body)),
    onSuccess: (deposit) => {
      attempt.reset();
      queryClient.setQueryData(usdtKeys.deposit(deposit.id), deposit);
      void queryClient.invalidateQueries({ queryKey: USDT_KEY });
      setCreated(deposit);
    },
  });
  const send = useSendOnce(mutation.mutate);

  if (created) return <DepositCreated initial={created} networks={networks} onDone={onDone} />;

  if (open.length === 0) {
    return (
      <p className="rounded-lg border border-surface-700 bg-surface-850 px-4 py-3 text-sm text-content-300">
        USDT deposits are paused on every network right now. Please try again later.
      </p>
    );
  }

  const problem =
    amountProblem(amountText, amount) ??
    (network && amount !== null && network.minDeposit > 0 && amount > 0 && amount < network.minDeposit
      ? 'The minimum deposit on ' + network.label + ' is ' + formatUsdt(network.minDeposit, { trim: true }) + '.'
      : null);
  const ready = !!network && amount !== null && amount > 0 && !problem;

  function onSubmit(event: FormEvent) {
    event.preventDefault();
    if (!ready || !network || amount === null || mutation.isPending) return;
    send({ network: network.code, amount });
  }

  return (
    <form onSubmit={onSubmit} className="space-y-4" noValidate>
      <div>
        <span className="label">Currency</span>
        <div className="flex items-center gap-2.5 rounded-lg border border-surface-700 bg-surface-850 px-3 py-2">
          <UsdtMark className="h-6 w-6 text-xs" />
          <span className="text-sm font-bold text-content-100">USDT</span>
          <span className="text-xs text-content-400">Tether USD</span>
        </div>
      </div>

      <NetworkPicker
        purpose="deposit"
        networks={open}
        value={code}
        onChange={(next) => {
          setCode(next);
          mutation.reset();
        }}
      />

      <AmountInput
        id="usdt-deposit-amount"
        value={amountText}
        onChange={(text) => {
          setAmountText(text);
          mutation.reset();
        }}
        error={problem}
        hint={
          network && network.minDeposit > 0 && !problem ? (
            <>
              Minimum on {network.label}:{' '}
              <span className="font-semibold text-content-200 tnum">
                {formatUsdt(network.minDeposit, { trim: true })}
              </span>
            </>
          ) : undefined
        }
      />

      <NetworkWarning />

      <ErrorNote message={mutation.isError ? usdtErrorMessage(mutation.error) : null} />

      <button type="submit" className="btn-primary w-full" disabled={!ready || mutation.isPending}>
        {mutation.isPending ? <Spinner /> : null}
        Continue
      </button>
      <p className="text-center text-[11px] leading-relaxed text-content-400">
        Continue opens a payment request. Your USDT wallet is credited once the payment is confirmed on the network.
      </p>
    </form>
  );
}

/** The deposit just created: what to pay, where, and the way to the checkout. */
function DepositCreated({
  initial,
  networks,
  onDone,
}: {
  initial: UsdtDepositView;
  networks: UsdtNetworkView[];
  onDone: () => void;
}) {
  const query = useUsdtDeposit(initial.id, initial);
  const deposit = query.data ?? initial;
  const label = networkLabel(deposit.network, networks);
  const paymentUrl = canStillPay(deposit.status) ? safeExternalUrl(deposit.paymentUrl) : null;

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-3">
        <UsdtMark />
        <div className="min-w-0 flex-1">
          <h3 className="text-sm font-bold text-content-100">Deposit created</h3>
          <p className="text-xs text-content-400">Reference {deposit.reference}</p>
        </div>
        <StatusBadge tone={depositStatusTone(deposit.status)}>{depositStatusWord(deposit.status)}</StatusBadge>
      </div>

      <p className="text-sm text-content-300">{depositStatusExplanation(deposit, label)}</p>

      <DetailList
        rows={[
          { label: 'Amount', value: formatUsdt(deposit.amountExpected), strong: true },
          { label: 'Network', value: label },
          { label: 'Status', value: depositStatusWord(deposit.status) },
          deposit.expiresAt ? { label: 'Pay before', value: formatDateTime(deposit.expiresAt) } : null,
        ]}
      />

      {deposit.payAddress && canStillPay(deposit.status) ? (
        <CopyValue label="Pay address" value={deposit.payAddress} />
      ) : null}

      <NetworkWarning />

      {paymentUrl ? (
        <div className="space-y-1.5">
          {/* Same tab: the checkout sends the customer back to the status
              page when it is done. */}
          <button type="button" className="btn-primary w-full" onClick={() => window.location.assign(paymentUrl)}>
            Open payment page
            <ExternalLink className="h-4 w-4" />
          </button>
          <p className="text-center text-[11px] text-content-400">
            Page did not open?{' '}
            <a href={paymentUrl} target="_blank" rel="noopener noreferrer" className="font-semibold text-accent underline">
              Open it in a new tab
            </a>
          </p>
        </div>
      ) : null}

      <button type="button" className="btn-ghost w-full" onClick={onDone}>
        Done
      </button>
    </div>
  );
}
