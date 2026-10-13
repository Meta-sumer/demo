import { useEffect, useMemo, useState, type FormEvent } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import clsx from 'clsx';
import { ArrowDownToLine, ArrowLeft, ArrowUpFromLine, Check, Copy, Paperclip, X } from 'lucide-react';
import { api } from '../services/api';
import { ApiRequestError } from '../services/http';
import { useAccountStore } from '../stores/accountStore';
import { formatMoney, fromMinor, toMinor } from '../utils/format';
import { toast } from '../hooks/useToast';
import { Panel, Spinner } from '../components/ui';
import { MethodPicker } from '../features/transfer/MethodPicker';
import type { PaymentMethod, PayoutDestination } from '../types/api';

const QUICK = [1_000_00, 5_000_00, 10_000_00, 25_000_00];
const MAX_PROOF_BYTES = 5 * 1024 * 1024;

/**
 * Manual deposit and withdrawal.
 *
 * Money moves outside the platform. A deposit is therefore a *claim*: the
 * customer pays to the account shown here, then submits the UTR and/or a
 * screenshot, and an operator credits the wallet only after checking it against
 * the bank. The copy on this page says so plainly, because a balance that
 * appears instantly and then disappears on rejection is worse than a wait.
 */
export function TransferPage({ kind }: { kind: 'DEPOSIT' | 'WITHDRAWAL' }) {
  const navigate = useNavigate();
  const onDone = () => navigate('/wallet');

  return kind === 'DEPOSIT' ? (
    <PageShell
      title="Deposit to Live account"
      subtitle="Pay to the account below, then submit the reference so we can verify it."
    >
      <div className="mt-5">
        <DepositForm onDone={onDone} />
      </div>
    </PageShell>
  ) : (
    <PageShell
      title="Withdraw from Live account"
      subtitle="The amount is reserved immediately and paid out once an operator approves it."
    >
      <div className="mt-5">
        <WithdrawForm onDone={onDone} />
      </div>
    </PageShell>
  );
}

function PageShell({ title, subtitle, children }: { title: string; subtitle: string; children: React.ReactNode }) {
  return (
    <div className="mx-auto max-w-lg space-y-3">
      <Link
        to="/account#funds"
        className="inline-flex items-center gap-1.5 text-sm text-content-400 hover:text-content-100"
      >
        <ArrowLeft className="h-4 w-4" />
        Back to account
      </Link>

      <Panel className="p-5">
        <h1 className="text-lg font-semibold text-content-100">{title}</h1>
        <p className="mt-1 text-sm text-content-400">{subtitle}</p>
        {children}
      </Panel>
    </div>
  );
}

function AmountField({ amountMinor, onChange }: { amountMinor: number; onChange: (minor: number) => void }) {
  return (
    <div>
      <label className="label" htmlFor="amount">
        Amount
      </label>
      <input
        id="amount"
        inputMode="decimal"
        className="field tnum"
        value={fromMinor(amountMinor)}
        onChange={(event) => {
          const parsed = Number(event.target.value.replace(/[^0-9.]/g, ''));
          if (Number.isFinite(parsed)) onChange(toMinor(parsed));
        }}
      />
      <div className="mt-2 flex flex-wrap gap-1">
        {QUICK.map((value) => (
          <button
            key={value}
            type="button"
            onClick={() => onChange(value)}
            className={
              'rounded-md px-2 py-1 text-[11px] font-medium transition ' +
              (amountMinor === value
                ? 'bg-accent text-surface-950'
                : 'bg-surface-800 text-content-300 hover:text-content-100')
            }
          >
            {formatMoney(value, 'INR', false)}
          </button>
        ))}
      </div>
    </div>
  );
}

/**
 * Says which way the money is going, first thing in the form.
 *
 * Deposit and withdrawal share every building block (brief UX-20), so this is
 * what keeps them from being mistaken for each other: its own icon, its own
 * tone and the words "into" / "out of". Both name the Live account - demo
 * balance is virtual and never moves through here.
 */
function KindBanner({ kind }: { kind: 'DEPOSIT' | 'WITHDRAWAL' }) {
  const deposit = kind === 'DEPOSIT';
  const Icon = deposit ? ArrowDownToLine : ArrowUpFromLine;
  return (
    <div
      className={clsx(
        'flex items-center gap-3 rounded-lg border px-3 py-2.5',
        deposit ? 'border-up/30 bg-up/10' : 'border-accent/30 bg-accent/10',
      )}
    >
      <span
        className={clsx(
          'grid h-8 w-8 shrink-0 place-items-center rounded-lg',
          deposit ? 'bg-up/15 text-up' : 'bg-accent/15 text-accent',
        )}
      >
        <Icon className="h-4 w-4" />
      </span>
      <span className="min-w-0 text-xs leading-snug">
        <span className={clsx('block text-sm font-bold', deposit ? 'text-up' : 'text-content-100')}>
          {deposit ? 'Deposit into your Live account' : 'Withdraw from your Live account'}
        </span>
        <span className="text-content-400">Real money. Your demo balance is not affected.</span>
      </span>
    </div>
  );
}

/** Limits line under the amount, for either direction. */
function LimitsHint({ limits }: { limits: { min: number; max: number } | null }) {
  if (!limits) return null;
  return (
    <p className="mt-1 text-[11px] text-content-400">
      Minimum <span className="font-semibold tnum text-content-200">{formatMoney(limits.min)}</span> · maximum{' '}
      <span className="font-semibold tnum text-content-200">{formatMoney(limits.max)}</span>
    </p>
  );
}

/**
 * The review step before a request is sent.
 *
 * Money requests are checked by hand and cannot be recalled once a payment is
 * made against them, so the customer sees exactly what will be submitted -
 * and can go back to change it - before anything reaches the server. The
 * buttons say what they do ("Confirm deposit", "Edit"), never "Continue".
 */
function ReviewStep({
  kind,
  rows,
  error,
  pending,
  onConfirm,
  onEdit,
  footnote,
}: {
  kind: 'DEPOSIT' | 'WITHDRAWAL';
  rows: Array<{ label: string; value: string; strong?: boolean }>;
  error: string | null;
  pending: boolean;
  onConfirm: () => void;
  onEdit: () => void;
  footnote: string;
}) {
  const deposit = kind === 'DEPOSIT';
  return (
    <div className="space-y-4">
      <KindBanner kind={kind} />
      <div>
        <h3 className="text-sm font-bold text-content-100">
          {deposit ? 'Check your deposit' : 'Check your withdrawal'}
        </h3>
        <dl className="mt-2 divide-y divide-surface-800 rounded-lg border border-surface-700 bg-surface-850">
          {rows.map((row) => (
            <div key={row.label} className="flex items-start justify-between gap-4 px-4 py-2.5">
              <dt className="shrink-0 text-xs text-content-400">{row.label}</dt>
              <dd
                className={clsx(
                  'min-w-0 break-words text-right text-sm tnum',
                  row.strong ? 'text-base font-bold text-content-100' : 'font-medium text-content-200',
                )}
              >
                {row.value}
              </dd>
            </div>
          ))}
        </dl>
      </div>

      {error ? (
        <p className="rounded-lg border border-down/30 bg-down/10 px-3 py-2 text-sm text-down">{error}</p>
      ) : null}

      <div className="grid grid-cols-[auto_1fr] gap-2">
        <button type="button" className="btn-ghost" onClick={onEdit} disabled={pending}>
          Edit
        </button>
        <button type="button" className="btn-primary" onClick={onConfirm} disabled={pending}>
          {pending ? <Spinner /> : null}
          {deposit ? 'Confirm deposit' : 'Confirm withdrawal'}
        </button>
      </div>

      <p className="text-center text-[11px] leading-relaxed text-content-400">{footnote}</p>
    </div>
  );
}

function destinationLine(method: PaymentMethod | null): string {
  if (!method) return '--';
  if (method.type === 'UPI') return method.label + ' · ' + (method.upiId ?? '--');
  return method.label + ' · ' + [method.accountNumber, method.ifsc].filter(Boolean).join(' · ');
}

/** Account details are copied, not retyped - a mistyped UPI id loses real money. */
function CopyRow({ label, value }: { label: string; value: string }) {
  const [copied, setCopied] = useState(false);

  return (
    <div className="flex items-center gap-2 border-t border-surface-800 py-2 first:border-t-0">
      <span className="w-28 shrink-0 text-[11px] uppercase tracking-wider text-content-400">{label}</span>
      <span className="min-w-0 flex-1 truncate font-mono text-sm text-content-100">{value}</span>
      <button
        type="button"
        className="rounded-md p-1.5 text-content-400 transition hover:bg-surface-800 hover:text-content-100"
        aria-label={'Copy ' + label}
        onClick={async () => {
          try {
            await navigator.clipboard.writeText(value);
            setCopied(true);
            window.setTimeout(() => setCopied(false), 1500);
          } catch {
            toast('error', 'Could not copy', 'Select the text and copy it manually');
          }
        }}
      >
        {copied ? <Check className="h-4 w-4 text-up" /> : <Copy className="h-4 w-4" />}
      </button>
    </div>
  );
}

/**
 * Deposit claim.
 *
 * Rendered both as a page and inside the modal, so it owns no navigation of
 * its own - the host decides where "done" goes.
 */
export function DepositForm({ onDone }: { onDone: () => void }) {
  const queryClient = useQueryClient();

  const [amountMinor, setAmountMinor] = useState(QUICK[0]!);
  const [methodKey, setMethodKey] = useState<string>('');
  const [utr, setUtr] = useState('');
  const [note, setNote] = useState('');
  const [proof, setProof] = useState<File | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [reviewing, setReviewing] = useState(false);

  const methodsQuery = useQuery({ queryKey: ['payment-methods'], queryFn: api.transfers.paymentMethods });
  const methods = useMemo(() => methodsQuery.data ?? [], [methodsQuery.data]);
  const selected = methods.find((m) => m.key === methodKey) ?? methods[0] ?? null;

  useEffect(() => {
    if (!methodKey && methods.length > 0) setMethodKey(methods[0]!.key);
  }, [methods, methodKey]);

  const mutation = useMutation({
    mutationFn: () =>
      api.transfers.deposit({
        amount: amountMinor,
        paymentMethodKey: selected?.key,
        utr: utr.trim() || undefined,
        customerNote: note.trim() || undefined,
        proof,
      }),
    onSuccess: (transfer) => {
      void queryClient.invalidateQueries({ queryKey: ['transfers'] });
      toast(
        'success',
        'Deposit submitted for review',
        transfer.reference + ' — your wallet is credited once an operator confirms the payment',
      );
      onDone();
    },
    onError: (err) => setError(err instanceof ApiRequestError ? err.message : 'Could not submit the deposit'),
  });

  function onSubmit(event: FormEvent) {
    event.preventDefault();
    setError(null);

    if (amountMinor <= 0) return setError('Enter an amount greater than zero');
    if (selected && (amountMinor < selected.minAmount || amountMinor > selected.maxAmount)) {
      return setError(
        'Deposits through ' +
          selected.label +
          ' must be between ' +
          formatMoney(selected.minAmount) +
          ' and ' +
          formatMoney(selected.maxAmount),
      );
    }
    // Either is accepted; without one there is nothing for an operator to check.
    if (!utr.trim() && !proof) {
      return setError('Enter the UTR / reference number of your payment, or attach a screenshot');
    }
    setReviewing(true);
  }

  if (reviewing) {
    return (
      <ReviewStep
        kind="DEPOSIT"
        rows={[
          { label: 'Amount', value: formatMoney(amountMinor), strong: true },
          { label: 'Paid to', value: destinationLine(selected) },
          { label: 'UTR / reference', value: utr.trim() ? utr.trim().toUpperCase() : 'Not entered' },
          { label: 'Screenshot', value: proof ? proof.name : 'None' },
          ...(note.trim() ? [{ label: 'Note', value: note.trim() }] : []),
          { label: 'Credited to', value: 'Live account, after review' },
        ]}
        error={error}
        pending={mutation.isPending}
        onConfirm={() => {
          setError(null);
          mutation.mutate();
        }}
        onEdit={() => {
          setError(null);
          setReviewing(false);
        }}
        footnote="Your wallet is credited only after an operator matches this against our bank statement."
      />
    );
  }

  return (
    <form onSubmit={onSubmit} className="space-y-4">
      <KindBanner kind="DEPOSIT" />

      <MethodPicker
        methods={methods}
        selectedKey={selected?.key ?? null}
        onSelect={setMethodKey}
        loading={methodsQuery.isLoading}
      />

      {selected ? (
        <div className="rounded-lg border border-surface-700 bg-surface-850 px-4 py-3">
          <div className="mb-1 text-[11px] font-semibold uppercase tracking-wider text-content-400">
            Step 1 — pay to
          </div>
          {selected.type === 'UPI' ? (
            <CopyRow label="UPI ID" value={selected.upiId ?? '--'} />
          ) : (
            <>
              <CopyRow label="Account name" value={selected.accountName ?? '--'} />
              <CopyRow label="Account no." value={selected.accountNumber ?? '--'} />
              <CopyRow label="IFSC" value={selected.ifsc ?? '--'} />
              {selected.bankName ? <CopyRow label="Bank" value={selected.bankName} /> : null}
            </>
          )}
          {selected.instructions ? (
            <p className="mt-2 border-t border-surface-800 pt-2 text-[11px] leading-relaxed text-content-400">
              {selected.instructions}
            </p>
          ) : null}
        </div>
      ) : null}

      <div>
        <AmountField amountMinor={amountMinor} onChange={setAmountMinor} />
        <LimitsHint limits={selected ? { min: selected.minAmount, max: selected.maxAmount } : null} />
      </div>

      <div className="rounded-lg border border-surface-700 bg-surface-850 px-4 py-3">
        <div className="mb-2 text-[11px] font-semibold uppercase tracking-wider text-content-400">
          Step 2 — prove the payment
        </div>

        <label className="label" htmlFor="utr">
          UTR / reference number
        </label>
        <input
          id="utr"
          className="field font-mono uppercase"
          placeholder="e.g. 412345678901"
          value={utr}
          onChange={(event) => setUtr(event.target.value)}
        />
        <p className="mt-1 text-[11px] text-content-400">
          The reference shown on your payment receipt. Each payment can only be submitted once.
        </p>

        <div className="mt-3">
          <span className="label">Screenshot (optional if you entered a UTR)</span>
          {proof ? (
            <div className="flex items-center gap-2 rounded-lg border border-surface-700 bg-surface-900 px-3 py-2">
              <Paperclip className="h-4 w-4 shrink-0 text-content-400" />
              <span className="min-w-0 flex-1 truncate text-xs text-content-200">{proof.name}</span>
              <span className="shrink-0 text-[11px] text-content-400">{Math.ceil(proof.size / 1024)} KB</span>
              <button
                type="button"
                className="rounded p-1 text-content-400 hover:text-down"
                aria-label="Remove attachment"
                onClick={() => setProof(null)}
              >
                <X className="h-4 w-4" />
              </button>
            </div>
          ) : (
            <input
              type="file"
              accept="image/png,image/jpeg,image/webp,application/pdf"
              className="field cursor-pointer file:mr-3 file:rounded file:border-0 file:bg-surface-700 file:px-3 file:py-1 file:text-xs file:text-content-100"
              onChange={(event) => {
                const file = event.target.files?.[0] ?? null;
                if (file && file.size > MAX_PROOF_BYTES) {
                  setError('Screenshots must be under 5 MB');
                  event.target.value = '';
                  return;
                }
                setError(null);
                setProof(file);
              }}
            />
          )}
        </div>
      </div>

      <div>
        <label className="label" htmlFor="note">
          Note (optional)
        </label>
        <input
          id="note"
          className="field"
          placeholder="Anything the reviewer should know"
          value={note}
          onChange={(event) => setNote(event.target.value)}
        />
      </div>

      {error ? (
        <p className="rounded-lg border border-down/30 bg-down/10 px-3 py-2 text-sm text-down">{error}</p>
      ) : null}

      <button type="submit" className="btn-primary w-full">
        Review deposit
      </button>

      <p className="text-center text-[11px] leading-relaxed text-content-400">
        Your wallet is credited only after an operator matches this against our bank statement. Submitting a
        reference you did not pay will simply be rejected.
      </p>
    </form>
  );
}

/** Withdrawal request. Same arrangement as DepositForm. */
export function WithdrawForm({ onDone }: { onDone: () => void }) {
  const queryClient = useQueryClient();
  // Withdrawals only ever come out of the live account, whichever one is
  // being traded on.
  const wallet = useAccountStore((s) => s.wallets.LIVE ?? null);

  const [amountMinor, setAmountMinor] = useState(QUICK[0]!);
  const [type, setType] = useState<'UPI' | 'BANK'>('UPI');
  const [upiId, setUpiId] = useState('');
  const [bank, setBank] = useState({ accountName: '', accountNumber: '', ifsc: '', bankName: '' });
  const [note, setNote] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [reviewing, setReviewing] = useState(false);

  const insufficient = !!wallet && wallet.availableBalance < amountMinor;

  // The same configured methods the deposit side lists. Payouts go out over
  // the same rails, so their limits are the ones that apply: the widest range
  // any method of the chosen kind accepts.
  const methodsQuery = useQuery({ queryKey: ['payment-methods'], queryFn: api.transfers.paymentMethods });
  const limits = useMemo(() => {
    const all = methodsQuery.data ?? [];
    const ofType = all.filter((method) => method.type === type);
    const pool = ofType.length > 0 ? ofType : all;
    if (pool.length === 0) return null;
    return {
      min: Math.min(...pool.map((method) => method.minAmount)),
      max: Math.max(...pool.map((method) => method.maxAmount)),
    };
  }, [methodsQuery.data, type]);

  const mutation = useMutation({
    mutationFn: () => {
      const destination: PayoutDestination =
        type === 'UPI'
          ? { type: 'UPI', upiId: upiId.trim() }
          : {
              type: 'BANK',
              accountName: bank.accountName.trim(),
              accountNumber: bank.accountNumber.trim(),
              ifsc: bank.ifsc.trim().toUpperCase(),
              bankName: bank.bankName.trim() || undefined,
            };

      return api.transfers.withdraw({
        amount: amountMinor,
        destination,
        customerNote: note.trim() || undefined,
      });
    },
    onSuccess: (transfer) => {
      void queryClient.invalidateQueries({ queryKey: ['wallet'] });
      void queryClient.invalidateQueries({ queryKey: ['transfers'] });
      toast('success', 'Withdrawal requested', transfer.reference + ' — the amount is reserved until it is paid');
      onDone();
    },
    onError: (err) => setError(err instanceof ApiRequestError ? err.message : 'Request failed'),
  });

  function onSubmit(event: FormEvent) {
    event.preventDefault();
    setError(null);
    if (amountMinor <= 0) return setError('Enter an amount greater than zero');
    if (limits && (amountMinor < limits.min || amountMinor > limits.max)) {
      return setError(
        'Withdrawals by ' +
          (type === 'UPI' ? 'UPI' : 'bank transfer') +
          ' must be between ' +
          formatMoney(limits.min) +
          ' and ' +
          formatMoney(limits.max),
      );
    }
    if (insufficient) return setError('Insufficient available balance');
    if (type === 'UPI' && !upiId.trim()) return setError('Enter the UPI ID to pay');
    if (type === 'BANK' && (!bank.accountName.trim() || !bank.accountNumber.trim() || !bank.ifsc.trim())) {
      return setError('Enter the account name, number and IFSC');
    }
    setReviewing(true);
  }

  if (reviewing) {
    const payTo =
      type === 'UPI'
        ? 'UPI · ' + upiId.trim()
        : [bank.accountName.trim(), bank.accountNumber.trim(), bank.ifsc.trim().toUpperCase(), bank.bankName.trim()]
            .filter(Boolean)
            .join(' · ');
    return (
      <ReviewStep
        kind="WITHDRAWAL"
        rows={[
          { label: 'Amount', value: formatMoney(amountMinor), strong: true },
          { label: 'Pay to', value: payTo },
          ...(note.trim() ? [{ label: 'Note', value: note.trim() }] : []),
          {
            label: 'Available after',
            value: wallet ? formatMoney(wallet.availableBalance - amountMinor, wallet.currency) : '--',
          },
        ]}
        error={error}
        pending={mutation.isPending}
        onConfirm={() => {
          setError(null);
          mutation.mutate();
        }}
        onEdit={() => {
          setError(null);
          setReviewing(false);
        }}
        footnote="The amount is reserved as soon as you confirm, and paid manually to exactly these details once approved."
      />
    );
  }

  return (
    <form onSubmit={onSubmit} className="space-y-4">
      <KindBanner kind="WITHDRAWAL" />

      <div>
        <AmountField amountMinor={amountMinor} onChange={setAmountMinor} />
        <LimitsHint limits={limits} />
      </div>

      <div>
        <span className="label">Pay me by</span>
        <div className="flex gap-1">
          {(['UPI', 'BANK'] as const).map((option) => (
            <button
              key={option}
              type="button"
              onClick={() => setType(option)}
              className={
                'rounded-lg border px-3 py-1.5 text-sm font-medium transition ' +
                (type === option
                  ? 'border-accent bg-accent/15 text-content-100'
                  : 'border-surface-700 bg-surface-850 text-content-300')
              }
            >
              {option === 'UPI' ? 'UPI' : 'Bank transfer'}
            </button>
          ))}
        </div>
      </div>

      {type === 'UPI' ? (
        <div>
          <label className="label" htmlFor="upiId">
            UPI ID
          </label>
          <input
            id="upiId"
            className="field font-mono"
            placeholder="yourname@bank"
            value={upiId}
            onChange={(event) => setUpiId(event.target.value)}
          />
        </div>
      ) : (
        <div className="grid gap-3 sm:grid-cols-2">
          <div className="sm:col-span-2">
            <label className="label" htmlFor="accountName">
              Account holder name
            </label>
            <input
              id="accountName"
              className="field"
              value={bank.accountName}
              onChange={(event) => setBank((b) => ({ ...b, accountName: event.target.value }))}
            />
          </div>
          <div>
            <label className="label" htmlFor="accountNumber">
              Account number
            </label>
            <input
              id="accountNumber"
              className="field font-mono"
              inputMode="numeric"
              value={bank.accountNumber}
              onChange={(event) => setBank((b) => ({ ...b, accountNumber: event.target.value }))}
            />
          </div>
          <div>
            <label className="label" htmlFor="ifsc">
              IFSC
            </label>
            <input
              id="ifsc"
              className="field font-mono uppercase"
              maxLength={11}
              value={bank.ifsc}
              onChange={(event) => setBank((b) => ({ ...b, ifsc: event.target.value }))}
            />
          </div>
          <div className="sm:col-span-2">
            <label className="label" htmlFor="bankName">
              Bank name (optional)
            </label>
            <input
              id="bankName"
              className="field"
              value={bank.bankName}
              onChange={(event) => setBank((b) => ({ ...b, bankName: event.target.value }))}
            />
          </div>
        </div>
      )}

      <div>
        <label className="label" htmlFor="wnote">
          Note (optional)
        </label>
        <input id="wnote" className="field" value={note} onChange={(event) => setNote(event.target.value)} />
      </div>

      {wallet ? (
        <p className="text-xs text-content-400">
          Available balance:{' '}
          <span className="font-semibold tnum text-content-200">
            {formatMoney(wallet.availableBalance, wallet.currency)}
          </span>
        </p>
      ) : null}

      {error ? (
        <p className="rounded-lg border border-down/30 bg-down/10 px-3 py-2 text-sm text-down">{error}</p>
      ) : null}

      <button type="submit" className="btn-primary w-full" disabled={insufficient}>
        Review withdrawal
      </button>

      <p className="text-center text-[11px] leading-relaxed text-content-400">
        Double-check these details. Payments are made manually to exactly what you enter here.
      </p>
    </form>
  );
}
