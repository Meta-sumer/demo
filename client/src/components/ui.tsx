import clsx from 'clsx';
import type { ReactNode } from 'react';
import { CheckCircle2, Info, XCircle } from 'lucide-react';
import { useToastStore } from '../hooks/useToast';

export function Panel({ className, children }: { className?: string; children: ReactNode }) {
  return <div className={clsx('panel', className)}>{children}</div>;
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
  tone?: 'neutral' | 'up' | 'down' | 'accent';
}) {
  const toneClass =
    tone === 'up' ? 'text-up' : tone === 'down' ? 'text-down' : tone === 'accent' ? 'text-accent' : 'text-content-100';
  return (
    <div className="panel px-4 py-3">
      <div className="text-[11px] font-medium uppercase tracking-wider text-content-400">{label}</div>
      <div className={clsx('mt-1 text-xl font-semibold tnum', toneClass)}>{value}</div>
      {hint ? <div className="mt-0.5 text-xs text-content-400">{hint}</div> : null}
    </div>
  );
}

const badgeTones: Record<string, string> = {
  neutral: 'bg-surface-700 text-content-200',
  up: 'bg-up/15 text-up ring-1 ring-up/30',
  down: 'bg-down/15 text-down ring-1 ring-down/30',
  warn: 'bg-warn/15 text-warn ring-1 ring-warn/30',
  accent: 'bg-accent/15 text-accent ring-1 ring-accent/30',
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

/** Feed status pill - the trader needs this visible at all times. */
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

export function Toaster() {
  const toasts = useToastStore((s) => s.toasts);
  const dismiss = useToastStore((s) => s.dismiss);

  return (
    <div className="pointer-events-none fixed bottom-[calc(3.5rem+env(safe-area-inset-bottom)+1rem)] right-4 z-50 flex w-[min(360px,calc(100vw-2rem))] lg:bottom-4 flex-col gap-2">
      {toasts.map((item) => {
        const Icon = item.tone === 'success' ? CheckCircle2 : item.tone === 'error' ? XCircle : Info;
        const tone =
          item.tone === 'success' ? 'text-up' : item.tone === 'error' ? 'text-down' : 'text-accent';
        return (
          <button
            key={item.id}
            onClick={() => dismiss(item.id)}
            className="panel pointer-events-auto flex w-full items-start gap-3 px-4 py-3 text-left animate-fade-in"
          >
            <Icon className={clsx('mt-0.5 h-4 w-4 shrink-0', tone)} />
            <span className="min-w-0">
              <span className="block text-sm font-medium text-content-100">{item.title}</span>
              {item.description ? (
                <span className="mt-0.5 block text-xs text-content-400">{item.description}</span>
              ) : null}
            </span>
          </button>
        );
      })}
    </div>
  );
}
