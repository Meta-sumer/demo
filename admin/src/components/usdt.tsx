import { useEffect, useState, type ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import clsx from 'clsx';
import { Check, ChevronDown, Copy, ExternalLink, RotateCcw } from 'lucide-react';
import { api } from '../services/api';
import { ApiRequestError } from '../services/http';
import { useAuthStore } from '../stores/authStore';
import { toast } from '../hooks/useToast';
import { formatDateTime } from '../utils/format';
import {
  depositStatusMeta,
  formatUsdt,
  networkLabel,
  processingStatusMeta,
  shortenMiddle,
  withdrawalStatusMeta,
  type StatusMeta,
} from '../utils/usdt';
import type { UsdtLedgerRow, UsdtUserSummary, WebhookEventView } from '../types/usdt';
import { Badge, EmptyState } from './ui';

/* ------------------------------------------------------------------ errors */

/**
 * The server's own message when it sent one; a safe generic line otherwise.
 * Raw transport text ("Request failed with status code 502") never reaches an
 * operator.
 */
export function errorText(err: unknown, fallback = 'Something went wrong. Try again.'): string {
  if (err instanceof ApiRequestError) {
    if (err.code !== 'NETWORK_ERROR') return err.message || fallback;
    if (err.message === 'Cannot reach the trading server') return err.message;
    if (err.status === 403) return 'You do not have permission for this action.';
    return fallback;
  }
  return fallback;
}

export function QueryError({ error, onRetry, what }: { error: unknown; onRetry: () => void; what: string }) {
  return (
    <div className="flex flex-col items-center gap-2 px-6 py-10 text-center" role="alert">
      <p className="text-sm font-medium text-content-200">Could not load {what}</p>
      <p className="max-w-md text-xs text-content-400">{errorText(error)}</p>
      <button className="btn-ghost mt-1 px-3 py-1.5 text-xs" onClick={onRetry}>
        <RotateCcw className="h-3.5 w-3.5" />
        Retry
      </button>
    </div>
  );
}

/* ---------------------------------------------------------------- badges */

const dotTone: Record<string, string> = {
  neutral: 'bg-content-400',
  up: 'bg-up',
  down: 'bg-down',
  warn: 'bg-warn',
  accent: 'bg-accent',
  critical: 'bg-critical',
};

/** Colour is never the only signal: the word is always printed beside it. */
function MetaBadge({ meta }: { meta: StatusMeta }) {
  return (
    <Badge tone={meta.tone} className="whitespace-nowrap">
      <span className={clsx('h-1.5 w-1.5 shrink-0 rounded-full', dotTone[meta.tone])} aria-hidden="true" />
      <span title={meta.description || undefined}>{meta.label}</span>
    </Badge>
  );
}

export function DepositStatusBadge({ status }: { status: string }) {
  return <MetaBadge meta={depositStatusMeta(status)} />;
}

export function WithdrawalStatusBadge({ status }: { status: string }) {
  return <MetaBadge meta={withdrawalStatusMeta(status)} />;
}

export function ProcessingStatusBadge({ status }: { status: string }) {
  return <MetaBadge meta={processingStatusMeta(status)} />;
}

export function NetworkBadge({ network }: { network: string | null | undefined }) {
  return (
    <Badge tone="neutral" className="font-mono">
      {networkLabel(network)}
    </Badge>
  );
}

export function ModeBadge({ mode }: { mode: string | null | undefined }) {
  if (!mode) return <span className="text-content-400">--</span>;
  const test = mode.toLowerCase() === 'test';
  return <Badge tone={test ? 'warn' : 'accent'}>{test ? 'Test mode' : 'Live'}</Badge>;
}

export function YesNo({ value, yes = 'Yes', no = 'No' }: { value: boolean; yes?: string; no?: string }) {
  return <MetaBadge meta={{ label: value ? yes : no, tone: value ? 'up' : 'neutral', description: '' }} />;
}

/* ----------------------------------------------------------------- money */

export function Usdt({
  micro,
  className,
  sign,
  wrap,
}: {
  micro: number | null | undefined;
  className?: string;
  sign?: boolean;
  /** Lets the unit drop to the next line in a narrow tile; the figure itself never breaks. */
  wrap?: boolean;
}) {
  if (!wrap) return <span className={clsx('whitespace-nowrap tnum', className)}>{formatUsdt(micro, { sign })}</span>;
  const figure = formatUsdt(micro, { sign, unit: false });
  return (
    <span className={clsx('tnum', className)}>
      {/* Breaks only if the figure alone is wider than its box - better than clipping a balance. */}
      <span className="[overflow-wrap:anywhere]">{figure}</span>
      {figure === '--' ? null : (
        <>
          {' '}
          <span className="whitespace-nowrap text-[0.75em] font-medium opacity-75">USDT</span>
        </>
      )}
    </span>
  );
}

/** Table cells: the figure only - the column header carries the unit. */
export function UsdtFigure({ micro, className }: { micro: number | null | undefined; className?: string }) {
  return <span className={clsx('whitespace-nowrap tnum', className)}>{formatUsdt(micro, { unit: false })}</span>;
}

/* ------------------------------------------------------------- copy text */

async function copyToClipboard(value: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(value);
    return true;
  } catch {
    // Older browsers / insecure origins: fall back to a hidden textarea.
    try {
      const area = document.createElement('textarea');
      area.value = value;
      area.setAttribute('readonly', '');
      area.style.position = 'fixed';
      area.style.opacity = '0';
      document.body.appendChild(area);
      area.select();
      const ok = document.execCommand('copy');
      area.remove();
      return ok;
    } catch {
      return false;
    }
  }
}

export function CopyButton({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    if (!copied) return;
    const timer = window.setTimeout(() => setCopied(false), 1500);
    return () => window.clearTimeout(timer);
  }, [copied]);

  return (
    <button
      type="button"
      className="rounded p-1 text-content-400 hover:bg-surface-800 hover:text-content-100"
      aria-label={copied ? label + ' copied' : 'Copy ' + label}
      title={copied ? 'Copied' : 'Copy ' + label}
      onClick={async (event) => {
        event.stopPropagation();
        if (await copyToClipboard(value)) setCopied(true);
        else toast('error', 'Could not copy', 'Select the text and copy it manually');
      }}
    >
      {copied ? <Check className="h-3.5 w-3.5 text-up" /> : <Copy className="h-3.5 w-3.5" />}
    </button>
  );
}

/** A long identifier: shortened in place, full value in the title, the copy and the link. */
export function CopyableText({
  value,
  label,
  href,
  short = true,
  head,
  tail,
  className,
}: {
  value: string | null | undefined;
  label: string;
  href?: string | null;
  short?: boolean;
  head?: number;
  tail?: number;
  className?: string;
}) {
  if (!value) return <span className="text-content-400">--</span>;
  const shown = short ? shortenMiddle(value, head, tail) : value;
  return (
    <span
      className={clsx(
        'inline-flex max-w-full items-center gap-0.5 font-mono text-[11px] text-content-200',
        short && 'whitespace-nowrap',
        className,
      )}
    >
      {href ? (
        <a
          href={href}
          target="_blank"
          rel="noopener noreferrer"
          className={clsx('inline-flex items-center gap-1 hover:text-accent hover:underline', !short && 'break-all')}
          title={value + ' - opens the block explorer'}
          onClick={(event) => event.stopPropagation()}
        >
          {shown}
          <ExternalLink className="h-3 w-3 shrink-0" aria-hidden="true" />
          <span className="sr-only">(opens the block explorer)</span>
        </a>
      ) : (
        <span title={value} className={clsx(!short && 'break-all')}>
          {shown}
        </span>
      )}
      <CopyButton value={value} label={label} />
    </span>
  );
}

/* ------------------------------------------------------------------ user */

export function UserCell({ user, fallbackId }: { user: UsdtUserSummary | null | undefined; fallbackId?: string }) {
  const can = useAuthStore((s) => s.can);
  if (!user) {
    return fallbackId ? (
      <span className="font-mono text-[11px] text-content-400">{fallbackId.slice(-8)}</span>
    ) : (
      <span className="text-content-400">--</span>
    );
  }
  return (
    <div className="min-w-0">
      {can('users:read') ? (
        <Link
          to={'/admin/users/' + user.id}
          className="block truncate font-medium text-content-100 hover:text-accent"
          onClick={(event) => event.stopPropagation()}
        >
          {user.username}
        </Link>
      ) : (
        <span className="block truncate font-medium text-content-100">{user.username}</span>
      )}
      <span className="block truncate text-[11px] text-content-400">{user.email}</span>
    </div>
  );
}

/* --------------------------------------------------------------- details */

export function DetailGrid({ items }: { items: Array<{ label: string; value: ReactNode; wide?: boolean }> }) {
  return (
    <dl className="grid grid-cols-1 gap-x-4 gap-y-3 sm:grid-cols-2">
      {items.map((item) => (
        <div key={item.label} className={clsx('min-w-0', item.wide && 'sm:col-span-2')}>
          <dt className="text-[10px] font-medium uppercase tracking-wider text-content-400">{item.label}</dt>
          <dd className="mt-0.5 min-w-0 break-words text-sm text-content-100">{item.value ?? '--'}</dd>
        </div>
      ))}
    </dl>
  );
}

export function Section({ title, description, children, actions }: {
  title: string;
  description?: string;
  children: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <section className="border-b border-surface-800 px-4 py-4 last:border-b-0">
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <div className="min-w-0">
          <h3 className="text-xs font-semibold uppercase tracking-wider text-content-300">{title}</h3>
          {description ? <p className="text-[11px] text-content-400">{description}</p> : null}
        </div>
        {actions ? <div className="ml-auto flex items-center gap-2">{actions}</div> : null}
      </div>
      {children}
    </section>
  );
}

export function dateOrDash(value: string | null | undefined): string {
  if (!value) return '--';
  const time = Date.parse(value);
  return Number.isNaN(time) ? value : formatDateTime(time);
}

/** Full timestamp with seconds and year, for detail views and audits. */
export function fullDate(value: string | null | undefined): string {
  if (!value) return '--';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString('en-GB', {
    day: '2-digit',
    month: 'short',
    year: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  });
}

/* ------------------------------------------------------------------- JSON */

function prettyJson(value: unknown): string {
  if (typeof value === 'string') {
    try {
      return JSON.stringify(JSON.parse(value), null, 2);
    } catch {
      return value;
    }
  }
  try {
    return JSON.stringify(value, null, 2) ?? 'null';
  } catch {
    return String(value);
  }
}

export function JsonBlock({ value, label = 'payload' }: { value: unknown; label?: string }) {
  const text = prettyJson(value);
  return (
    <div className="relative">
      <div className="absolute right-1.5 top-1.5">
        <CopyButton value={text} label={label} />
      </div>
      {/* Wrapped, not scrolled sideways: inside a table row a long line would otherwise widen the whole table. */}
      <pre
        className={clsx(
          'max-h-80 overflow-auto whitespace-pre-wrap rounded-lg border border-surface-800 bg-surface-950 p-3 pr-9',
          'font-mono text-[11px] leading-relaxed text-content-200 [overflow-wrap:anywhere]',
        )}
      >
        {text}
      </pre>
    </div>
  );
}

/* --------------------------------------------------------- events, ledger */

export function WebhookEventList({ events }: { events: WebhookEventView[] }) {
  const [open, setOpen] = useState<string | null>(null);
  if (events.length === 0) return <EmptyState title="No webhook events yet" description="BeeX has not called back for this record." />;

  return (
    <ul className="divide-y divide-surface-800 rounded-lg border border-surface-800">
      {events.map((event) => (
        <li key={event.id}>
          <button
            type="button"
            className="flex w-full flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2 text-left hover:bg-surface-850"
            aria-expanded={open === event.id}
            onClick={() => setOpen(open === event.id ? null : event.id)}
          >
            <span className="font-mono text-[11px] text-content-100">{event.event}</span>
            {event.status ? <span className="text-[11px] text-content-300">status {event.status}</span> : null}
            <ProcessingStatusBadge status={event.processingStatus} />
            <span className="ml-auto text-[11px] text-content-400">{fullDate(event.receivedAt)}</span>
            <ChevronDown
              className={clsx('h-3.5 w-3.5 text-content-400 transition', open === event.id && 'rotate-180')}
              aria-hidden="true"
            />
            {event.error ? <span className="w-full text-[11px] text-down">Error: {event.error}</span> : null}
          </button>
          {open === event.id ? (
            <div className="space-y-2 bg-surface-850/50 px-3 pb-3 pt-1">
              <div className="flex flex-wrap gap-x-4 gap-y-1 text-[11px] text-content-400">
                <span>Attempts {event.attempts}</span>
                <span>Processed {fullDate(event.processedAt)}</span>
                {event.mode ? <span>Mode {event.mode}</span> : null}
                <span className="font-mono">Key {event.eventKey}</span>
              </div>
              <JsonBlock value={event.payload} label="raw payload" />
            </div>
          ) : null}
        </li>
      ))}
    </ul>
  );
}

function ledgerAmount(row: UsdtLedgerRow, micro: number | null | undefined): string {
  return formatUsdt(micro, { unit: !row.currency || row.currency.toUpperCase().startsWith('USDT') });
}

export function LedgerTable({ rows }: { rows: UsdtLedgerRow[] }) {
  if (rows.length === 0) {
    return <EmptyState title="No ledger rows" description="Nothing has moved on the USDT wallet for this record." />;
  }

  return (
    <div className="relative overflow-x-auto rounded-lg border border-surface-800">
      <table className="table min-w-[760px]">
        <thead>
          <tr>
            <th>Date</th>
            <th>Type</th>
            <th>Movement</th>
            <th className="text-right">Amount</th>
            <th className="text-right">Fee</th>
            <th>Network</th>
            <th className="text-right">Balance after</th>
            <th className="text-right">Locked after</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row, index) => (
            <tr key={row._id ?? row.id ?? index}>
              <td className="whitespace-nowrap text-[11px] text-content-400">{fullDate(row.createdAt)}</td>
              <td className="text-[11px] text-content-200">
                <div className="font-mono">{row.type}</div>
                {row.description ? <div className="max-w-[220px] text-[10px] text-content-400">{row.description}</div> : null}
              </td>
              <td>
                <Badge tone={row.movement === 'CREDIT' ? 'up' : row.movement === 'DEBIT' ? 'down' : 'neutral'}>
                  {row.movement === 'CREDIT' ? 'Credit' : row.movement === 'DEBIT' ? 'Debit' : humanizeMovement(row.movement)}
                </Badge>
              </td>
              <td className="whitespace-nowrap text-right font-semibold tnum text-content-100">{ledgerAmount(row, row.amount)}</td>
              <td className="whitespace-nowrap text-right tnum text-content-300">
                {row.fee != null ? ledgerAmount(row, row.fee) : '--'}
              </td>
              <td>{row.network ? <NetworkBadge network={row.network} /> : <span className="text-content-400">--</span>}</td>
              <td className="whitespace-nowrap text-right tnum text-content-300">
                {row.balanceAfter != null ? ledgerAmount(row, row.balanceAfter) : '--'}
              </td>
              <td className="whitespace-nowrap text-right tnum text-content-300">
                {row.lockedAfter != null ? ledgerAmount(row, row.lockedAfter) : '--'}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function humanizeMovement(value: string): string {
  return value ? value.charAt(0).toUpperCase() + value.slice(1).toLowerCase().replace(/_/g, ' ') : '--';
}

/* --------------------------------------------------------------- filters */

export function useDebounced<T>(value: T, delayMs = 350): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timer = window.setTimeout(() => setDebounced(value), delayMs);
    return () => window.clearTimeout(timer);
  }, [value, delayMs]);
  return debounced;
}

export function FilterField({ label, htmlFor, children, className }: {
  label: string;
  htmlFor: string;
  children: ReactNode;
  className?: string;
}) {
  return (
    <div className={clsx('min-w-0', className)}>
      <label className="label" htmlFor={htmlFor}>
        {label}
      </label>
      {children}
    </div>
  );
}

/** A labelled on/off switch: the state is spelled out, not just coloured. */
export function Switch({
  checked,
  onChange,
  disabled,
  label,
  busy,
}: {
  checked: boolean;
  onChange: (next: boolean) => void;
  disabled?: boolean;
  label: string;
  busy?: boolean;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled || busy}
      onClick={() => onChange(!checked)}
      className="group inline-flex items-center gap-2 disabled:cursor-not-allowed disabled:opacity-60"
    >
      <span
        className={clsx(
          'relative inline-flex h-5 w-9 shrink-0 items-center rounded-full border transition',
          checked ? 'border-up/50 bg-up/30' : 'border-surface-600 bg-surface-800',
        )}
      >
        <span
          className={clsx(
            'inline-block h-3.5 w-3.5 rounded-full shadow transition',
            checked ? 'translate-x-[18px] bg-up' : 'translate-x-[3px] bg-content-400',
          )}
        />
      </span>
      <span className={clsx('text-xs font-semibold', checked ? 'text-up' : 'text-content-400')}>
        {busy ? 'Saving…' : checked ? 'Enabled' : 'Disabled'}
      </span>
    </button>
  );
}

/* ---------------------------------------------------------------- lists */

const FALLBACK_NETWORKS = [
  { code: 'USDTTRC20', label: 'TRC20' },
  { code: 'USDTBSC', label: 'BSC' },
];

export function useUsdtNetworks() {
  return useQuery({ queryKey: ['usdt', 'networks'], queryFn: api.usdt.networks, staleTime: 60_000 });
}

/** Filters by network code; falls back to the two known networks while the list loads. */
export function NetworkSelect({ id, value, onChange }: { id: string; value: string; onChange: (value: string) => void }) {
  const networks = useUsdtNetworks().data ?? FALLBACK_NETWORKS;
  return (
    <select id={id} className="field py-1.5" value={value} onChange={(event) => onChange(event.target.value)}>
      <option value="">All networks</option>
      {networks.map((network) => (
        <option key={network.code} value={network.code}>
          {network.label}
        </option>
      ))}
    </select>
  );
}

export function ListFooter({
  total,
  noun,
  page,
  totalPages,
  onPage,
  fetching,
}: {
  total: number;
  noun: string;
  page: number;
  totalPages: number;
  onPage: (page: number) => void;
  fetching?: boolean;
}) {
  return (
    <div className="flex flex-wrap items-center gap-3 border-t border-surface-800 px-4 py-2.5">
      <span className="text-[11px] text-content-400 tnum" aria-live="polite">
        {total.toLocaleString('en-US')} {noun}
        {total === 1 ? '' : 's'}
        {fetching ? ' · refreshing…' : ''}
      </span>
      {totalPages > 1 ? (
        <div className="ml-auto flex items-center gap-2">
          <button className="btn-ghost px-3 py-1 text-xs" disabled={page <= 1} onClick={() => onPage(page - 1)}>
            Previous
          </button>
          <span className="text-xs text-content-400 tnum">
            {page} / {totalPages}
          </span>
          <button className="btn-ghost px-3 py-1 text-xs" disabled={page >= totalPages} onClick={() => onPage(page + 1)}>
            Next
          </button>
        </div>
      ) : null}
    </div>
  );
}

/** The row actions stay in view while a wide table scrolls sideways. */
export const STICKY_ACTION_TH = 'sticky right-0 z-[1] bg-surface-900';
export const STICKY_ACTION_TD =
  'sticky right-0 z-[1] bg-surface-900 shadow-[-10px_0_12px_-12px_rgb(0_0_0/0.7)] group-hover:bg-surface-850';
