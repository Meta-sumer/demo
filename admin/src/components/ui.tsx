import clsx from 'clsx';
import type { ReactNode } from 'react';
import { CheckCircle2, Info, XCircle } from 'lucide-react';
import { useToastStore } from '../hooks/useToast';

export function Panel({ className, children }: { className?: string; children: ReactNode }) {
  return <div className={clsx('panel', className)}>{children}</div>;
}

export function PanelHeader({
  title,
  description,
  actions,
}: {
  title: string;
  description?: string;
  actions?: ReactNode;
}) {
  return (
    <div className="flex flex-wrap items-center gap-3 border-b border-surface-800 px-4 py-3">
      <div className="min-w-0">
        <h2 className="text-sm font-semibold text-content-100">{title}</h2>
        {description ? <p className="mt-0.5 text-xs text-content-400">{description}</p> : null}
      </div>
      {actions ? <div className="ml-auto flex items-center gap-2">{actions}</div> : null}
    </div>
  );
}

export function Stat({
  label,
  value,
  hint,
  tone = 'neutral',
}: {
  label: string;
  value: ReactNode;
  hint?: ReactNode;
  tone?: 'neutral' | 'up' | 'down' | 'accent' | 'warn';
}) {
  const toneClass = {
    neutral: 'text-content-100',
    up: 'text-up',
    down: 'text-down',
    accent: 'text-accent',
    warn: 'text-warn',
  }[tone];

  return (
    <div className="panel px-4 py-3">
      <div className="text-[11px] font-medium uppercase tracking-wider text-content-400">{label}</div>
      <div className={clsx('mt-1 text-xl font-semibold tnum', toneClass)}>{value}</div>
      {hint ? <div className="mt-0.5 text-[11px] text-content-400">{hint}</div> : null}
    </div>
  );
}

const badgeTones: Record<string, string> = {
  neutral: 'bg-surface-700 text-content-200',
  up: 'bg-up/15 text-up ring-1 ring-up/30',
  down: 'bg-down/15 text-down ring-1 ring-down/30',
  warn: 'bg-warn/15 text-warn ring-1 ring-warn/30',
  accent: 'bg-accent/15 text-accent ring-1 ring-accent/30',
  critical: 'bg-critical/20 text-critical ring-1 ring-critical/40',
};

export function Badge({
  children,
  tone = 'neutral',
  className,
}: {
  children: ReactNode;
  tone?: keyof typeof badgeTones;
  className?: string;
}) {
  return (
    <span
      className={clsx(
        'inline-flex items-center gap-1 rounded-md px-2 py-0.5 text-[11px] font-semibold',
        badgeTones[tone],
        className,
      )}
    >
      {children}
    </span>
  );
}

/** Live (real money) or Demo (practice money) account. */
export function AccountBadge({ accountType }: { accountType?: string }) {
  return accountType === 'DEMO' ? <Badge tone="warn">Demo</Badge> : <Badge tone="accent">Live</Badge>;
}

/** Risk level pill - the single most repeated element in this console. */
export function RiskBadge({ level }: { level: string }) {
  const tone =
    level === 'CRITICAL' ? 'critical' : level === 'HIGH' ? 'down' : level === 'MEDIUM' ? 'warn' : 'up';
  return <Badge tone={tone}>{level}</Badge>;
}

export function FeedBadge({ status, latencyMs }: { status: string; latencyMs?: number | null }) {
  const tone = status === 'HEALTHY' ? 'up' : status === 'DEGRADED' ? 'warn' : 'down';
  const dot = status === 'HEALTHY' ? 'bg-up' : status === 'DEGRADED' ? 'bg-warn' : 'bg-down';
  return (
    <Badge tone={tone}>
      <span className={clsx('h-1.5 w-1.5 rounded-full', dot)} />
      {status}
      {latencyMs != null ? <span className="tnum opacity-80">{Math.round(latencyMs)}ms</span> : null}
    </Badge>
  );
}

export function StatusBadge({ status }: { status: string }) {
  const tone =
    status === 'COMPLETED' || status === 'FILLED' || status === 'WON' || status === 'ACTIVE' || status === 'OK'
      ? 'up'
      : status === 'REJECTED' || status === 'FAILED' || status === 'LOST' || status === 'ALERT' || status === 'SUSPENDED'
        ? 'down'
        : status === 'PENDING' || status === 'PARTIALLY_FILLED' || status === 'SETTLING'
          ? 'warn'
          : 'neutral';
  return <Badge tone={tone}>{status}</Badge>;
}

export function Skeleton({ className }: { className?: string }) {
  return <div className={clsx('skeleton', className)} />;
}

export function Spinner({ className }: { className?: string }) {
  return (
    <span
      className={clsx('inline-block h-4 w-4 animate-spin rounded-full border-2 border-current border-t-transparent', className)}
      role="status"
      aria-label="Loading"
    />
  );
}

export function EmptyState({ title, description }: { title: string; description?: string }) {
  return (
    <div className="flex flex-col items-center justify-center gap-1 px-6 py-12 text-center">
      <p className="text-sm font-medium text-content-200">{title}</p>
      {description ? <p className="max-w-sm text-xs text-content-400">{description}</p> : null}
    </div>
  );
}

export function TableSkeleton({ rows = 8, columns = 6 }: { rows?: number; columns?: number }) {
  return (
    <div className="space-y-2 p-3">
      {Array.from({ length: rows }).map((_, rowIndex) => (
        <div key={rowIndex} className="flex gap-2">
          {Array.from({ length: columns }).map((__, columnIndex) => (
            <Skeleton key={columnIndex} className="h-8 flex-1" />
          ))}
        </div>
      ))}
    </div>
  );
}

export function Pagination({
  page,
  totalPages,
  onChange,
}: {
  page: number;
  totalPages: number;
  onChange: (page: number) => void;
}) {
  if (totalPages <= 1) return null;
  return (
    <div className="flex items-center justify-center gap-2 border-t border-surface-800 px-4 py-3">
      <button className="btn-ghost" disabled={page <= 1} onClick={() => onChange(page - 1)}>
        Previous
      </button>
      <span className="text-xs text-content-400 tnum">
        {page} / {totalPages}
      </span>
      <button className="btn-ghost" disabled={page >= totalPages} onClick={() => onChange(page + 1)}>
        Next
      </button>
    </div>
  );
}

export function Toaster() {
  const toasts = useToastStore((s) => s.toasts);
  const dismiss = useToastStore((s) => s.dismiss);

  return (
    <div className="pointer-events-none fixed bottom-4 right-4 z-50 flex w-[min(380px,calc(100vw-2rem))] flex-col gap-2">
      {toasts.map((item) => {
        const Icon = item.tone === 'success' ? CheckCircle2 : item.tone === 'error' ? XCircle : Info;
        const tone = item.tone === 'success' ? 'text-up' : item.tone === 'error' ? 'text-down' : 'text-accent';
        return (
          <button
            key={item.id}
            onClick={() => dismiss(item.id)}
            className="panel pointer-events-auto flex w-full items-start gap-3 px-4 py-3 text-left animate-fade-in"
          >
            <Icon className={clsx('mt-0.5 h-4 w-4 shrink-0', tone)} />
            <span className="min-w-0">
              <span className="block text-sm font-medium text-content-100">{item.title}</span>
              {item.description ? <span className="mt-0.5 block text-xs text-content-400">{item.description}</span> : null}
            </span>
          </button>
        );
      })}
    </div>
  );
}
