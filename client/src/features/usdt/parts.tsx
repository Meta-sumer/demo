import { useState, type ReactNode } from 'react';
import { Link } from 'react-router-dom';
import clsx from 'clsx';
import { AlertTriangle, Check, ChevronRight, Copy } from 'lucide-react';
import { toast } from '../../hooks/useToast';
import { Badge } from '../../components/ui';
import { formatUsdt, toUsdtInput } from '../../utils/usdt';
import type { UsdtNetworkView } from '../../types/api';
import { useUsdtOverview } from './queries';
import type { BadgeTone } from './model';

/** The USDT glyph, in Tether's green family (the theme's up tone, so it follows the theme). */
export function UsdtMark({ className }: { className?: string }) {
  return (
    <span
      aria-hidden
      className={clsx(
        'grid shrink-0 place-items-center rounded-full bg-up/15 font-black text-up ring-1 ring-up/30',
        className ?? 'h-8 w-8 text-sm',
      )}
    >
      ₮
    </span>
  );
}

/**
 * A USDT figure for a headline: whole units and cents at full strength, the
 * remaining four decimals quieter. Every digit is still there - the ledger
 * holds six - but the eye lands on the part people read.
 */
export function UsdtFigure({ micro, className }: { micro: number; className?: string }) {
  const text = formatUsdt(micro, { unit: false });
  const point = text.indexOf('.');
  const head = point >= 0 ? text.slice(0, point + 3) : text;
  const tail = point >= 0 ? text.slice(point + 3) : '';
  return (
    <span className={clsx('tnum', className)}>
      {head}
      <span className="text-content-400">{tail}</span>
      <span className="ml-1 text-[0.55em] font-semibold text-content-400">USDT</span>
    </span>
  );
}

export function StatusBadge({ tone, children }: { tone: BadgeTone; children: ReactNode }) {
  return <Badge tone={tone}>{children}</Badge>;
}

/** Network label as a small chip: "TRC20". */
export function NetworkChip({ label }: { label: string }) {
  return (
    <span className="inline-flex shrink-0 items-center rounded border border-surface-700 bg-surface-850 px-1.5 py-px text-[10px] font-bold tracking-wide text-content-300">
      {label}
    </span>
  );
}

export function ErrorNote({ message }: { message: string | null }) {
  if (!message) return null;
  return (
    <p role="alert" className="rounded-lg border border-down/30 bg-down/10 px-3 py-2 text-sm text-down">
      {message}
    </p>
  );
}

const NETWORK_WARNING =
  'Only send USDT on the selected network. Sending another token or using another network may result in permanent loss.';

/** The same-network warning, wherever an address or a network is chosen. */
export function NetworkWarning({ children }: { children?: ReactNode }) {
  return (
    <div className="flex gap-2.5 rounded-lg border border-warn/30 bg-warn/10 px-3 py-2.5">
      <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-warn" aria-hidden />
      <p className="text-xs leading-relaxed text-content-200">
        {children ?? NETWORK_WARNING}
      </p>
    </div>
  );
}

/**
 * Network choice as cards. Only networks open for this purpose are passed in;
 * a single one is still shown, so the customer always sees which chain the
 * money is on.
 */
export function NetworkPicker({
  networks,
  value,
  onChange,
  purpose,
}: {
  networks: UsdtNetworkView[];
  value: string;
  onChange: (code: string) => void;
  purpose: 'deposit' | 'withdraw';
}) {
  return (
    <div>
      <span className="label" id={'usdt-network-' + purpose}>
        Network
      </span>
      <div role="radiogroup" aria-labelledby={'usdt-network-' + purpose} className="grid grid-cols-2 gap-2">
        {networks.map((network) => {
          const selected = network.code === value;
          return (
            <button
              key={network.code}
              type="button"
              role="radio"
              aria-checked={selected}
              onClick={() => onChange(network.code)}
              className={clsx(
                'min-w-0 rounded-lg border px-3 py-2.5 text-left transition',
                selected
                  ? 'border-accent bg-accent/10 ring-1 ring-accent/40'
                  : 'border-surface-700 bg-surface-850 hover:border-surface-600',
              )}
            >
              <span className="flex items-center justify-between gap-2">
                <span className="text-sm font-bold text-content-100">{network.label}</span>
                <span
                  aria-hidden
                  className={clsx(
                    'grid h-4 w-4 shrink-0 place-items-center rounded-full border',
                    selected ? 'border-accent bg-accent' : 'border-surface-600',
                  )}
                >
                  {selected ? <span className="h-1.5 w-1.5 rounded-full bg-surface-950" /> : null}
                </span>
              </span>
              <span className="mt-0.5 block truncate text-[11px] text-content-400">{network.name || network.chain}</span>
              {purpose === 'deposit' && network.minDeposit > 0 ? (
                <span className="mt-0.5 block text-[11px] text-content-400 tnum">
                  Min {formatUsdt(network.minDeposit, { trim: true })}
                </span>
              ) : null}
            </button>
          );
        })}
      </div>
    </div>
  );
}

/**
 * A USDT amount, typed as text and parsed exactly by the caller. "USDT" sits
 * inside the field so the unit is never in doubt; Max, when offered, fills in
 * the whole available balance.
 */
export function AmountInput({
  id,
  label = 'Amount',
  value,
  onChange,
  error,
  hint,
  max,
}: {
  id: string;
  label?: string;
  value: string;
  onChange: (text: string) => void;
  error?: string | null;
  hint?: ReactNode;
  /** Available balance in micro-USDT; shows the Max button. */
  max?: number;
}) {
  const describedBy = [error ? id + '-error' : null, hint ? id + '-hint' : null].filter(Boolean).join(' ');
  return (
    <div>
      <label className="label" htmlFor={id}>
        {label}
      </label>
      <div className="relative">
        <input
          id={id}
          inputMode="decimal"
          autoComplete="off"
          spellCheck={false}
          placeholder="0.00"
          className={clsx(
            'field tnum text-base',
            max !== undefined ? 'pr-28' : 'pr-16',
            error && 'border-down focus:border-down focus:ring-down/25',
          )}
          value={value}
          aria-invalid={error ? true : undefined}
          aria-describedby={describedBy || undefined}
          onChange={(event) => onChange(event.target.value)}
        />
        <span className="pointer-events-none absolute inset-y-0 right-3 flex items-center text-xs font-semibold text-content-400">
          USDT
        </span>
        {max !== undefined ? (
          <button
            type="button"
            onClick={() => onChange(toUsdtInput(max))}
            disabled={max <= 0}
            className="absolute inset-y-1.5 right-14 rounded-md bg-surface-700 px-2 text-[11px] font-bold text-content-100 transition hover:brightness-110 disabled:opacity-45"
            aria-label={'Use the maximum, ' + formatUsdt(max)}
          >
            Max
          </button>
        ) : null}
      </div>
      {error ? (
        <p id={id + '-error'} className="mt-1 text-[11px] text-down">
          {error}
        </p>
      ) : null}
      {hint ? (
        <div id={id + '-hint'} className="mt-1 text-[11px] text-content-400">
          {hint}
        </div>
      ) : null}
    </div>
  );
}

export interface DetailRow {
  label: ReactNode;
  value: ReactNode;
  strong?: boolean;
  mono?: boolean;
}

/** Label / value pairs in a bordered list - every review and record screen uses it. */
export function DetailList({ rows, className }: { rows: Array<DetailRow | null | false>; className?: string }) {
  return (
    <dl
      className={clsx('divide-y divide-surface-800 rounded-lg border border-surface-700 bg-surface-850', className)}
    >
      {rows.filter((row): row is DetailRow => !!row).map((row, index) => (
        <div key={index} className="flex items-start justify-between gap-4 px-3.5 py-2.5">
          <dt className="shrink-0 text-xs text-content-400">{row.label}</dt>
          <dd
            className={clsx(
              'min-w-0 text-right tnum',
              row.mono ? 'break-all font-mono text-xs' : 'break-words text-sm',
              row.strong ? 'text-base font-bold text-content-100' : 'font-medium text-content-200',
            )}
          >
            {row.value}
          </dd>
        </div>
      ))}
    </dl>
  );
}

/** An address or a hash, in full, with a copy button - these are pasted elsewhere, never retyped. */
export function CopyValue({ label, value }: { label: string; value: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="flex items-start gap-2 rounded-lg border border-surface-700 bg-surface-850 px-3 py-2.5">
      <div className="min-w-0 flex-1">
        <div className="text-[11px] font-medium uppercase tracking-wider text-content-400">{label}</div>
        <div className="mt-0.5 break-all font-mono text-sm text-content-100">{value}</div>
      </div>
      <button
        type="button"
        aria-label={'Copy ' + label.toLowerCase()}
        className="shrink-0 rounded-md p-1.5 text-content-400 transition hover:bg-surface-800 hover:text-content-100"
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
 * A door to the USDT wallet from the places that list balances (Account ›
 * Funds, Transactions). Shows what is available there once it has loaded;
 * if the server does not offer USDT yet the door still opens, onto the page
 * that says so.
 */
export function UsdtWalletLink({ className }: { className?: string }) {
  const overview = useUsdtOverview();
  const data = overview.data;
  const status = !data
    ? overview.isError
      ? 'Deposit and withdraw USDT on TRC20 or BSC'
      : null
    : !data.enabled
      ? 'Not available yet'
      : data.wallet
        ? 'Available ' + formatUsdt(data.wallet.availableBalance, { trim: true })
        : 'Deposit and withdraw USDT on TRC20 or BSC';

  return (
    <Link
      to="/wallet/usdt"
      className={clsx(
        'panel flex items-center gap-3 px-4 py-3 transition hover:border-surface-600 hover:bg-surface-850',
        className,
      )}
    >
      <UsdtMark />
      <span className="min-w-0 flex-1">
        <span className="block text-sm font-semibold text-content-100">USDT wallet</span>
        <span className="block truncate text-[11px] text-content-400 tnum">
          {status ?? <span className="inline-block h-3 w-28 animate-pulse rounded bg-surface-800 align-middle" />}
        </span>
      </span>
      <span className="hidden text-[11px] text-content-400 sm:block">Separate from INR</span>
      <ChevronRight className="h-4 w-4 shrink-0 text-content-400" />
    </Link>
  );
}
