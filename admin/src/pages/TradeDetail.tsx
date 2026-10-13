import { useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ArrowLeft } from 'lucide-react';
import { api } from '../services/api';
import { ApiRequestError } from '../services/http';
import { useAuthStore } from '../stores/authStore';
import { toast } from '../hooks/useToast';
import { formatDateTime, formatMoney, formatPrice, formatSignedMoney } from '../utils/format';
import { Badge, Panel, PanelHeader, Skeleton, StatusBadge } from '../components/ui';

/** Full trade record, including the terms it was accepted under. */
export function TradeDetailPage() {
  const { id = '' } = useParams();
  const queryClient = useQueryClient();
  const can = useAuthStore((s) => s.can);
  const [reason, setReason] = useState('');

  const query = useQuery({ queryKey: ['admin', 'trade', id], queryFn: () => api.trades.detail(id) });

  const cancelMutation = useMutation({
    mutationFn: () => api.trades.cancel(id, reason),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['admin', 'trade', id] });
      setReason('');
      toast('success', 'Trade cancelled', 'The stake has been returned in full');
    },
    onError: (err) => toast('error', 'Cancel failed', err instanceof ApiRequestError ? err.message : undefined),
  });

  if (query.isLoading || !query.data) return <Skeleton className="h-96 w-full" />;
  const trade = query.data;
  const open = trade.status === 'OPEN' || trade.status === 'SETTLING';
  const platformResult = trade.status === 'WON' ? -trade.profit : trade.status === 'LOST' ? trade.stake : 0;

  return (
    <div className="mx-auto max-w-4xl space-y-4">
      <Link
        to="/admin/binary/trades"
        className="inline-flex items-center gap-1.5 text-sm text-content-400 hover:text-content-100"
      >
        <ArrowLeft className="h-4 w-4" />
        Back to trades
      </Link>

      <Panel className="overflow-hidden">
        <div className="flex flex-wrap items-center gap-3 border-b border-surface-800 px-4 py-3">
          <Badge tone={trade.direction === 'UP' ? 'up' : 'down'}>{trade.direction}</Badge>
          <div>
            <h1 className="text-lg font-semibold text-content-100">{trade.symbol}</h1>
            <p className="font-mono text-[11px] text-content-400">{trade.reference}</p>
          </div>
          <div className="ml-auto flex items-center gap-3">
            <StatusBadge status={trade.status} />
            <span
              className={
                'text-lg font-bold tnum ' +
                (platformResult > 0 ? 'text-up' : platformResult < 0 ? 'text-down' : 'text-content-300')
              }
            >
              {open ? '--' : formatSignedMoney(platformResult)}
            </span>
          </div>
        </div>

        <dl className="grid grid-cols-2 gap-px bg-surface-800/60 sm:grid-cols-4">
          <Field label="Customer" value={trade.userId} mono />
          <Field label="Stake" value={formatMoney(trade.stake)} />
          <Field label="Payout" value={trade.payoutPercent + '%'} />
          <Field label="Profit if won" value={formatMoney(trade.profit)} />
          <Field label="Entry price" value={formatPrice(trade.entryPrice, 5)} mono />
          <Field
            label="Expiry price"
            value={trade.expiryPrice !== null ? formatPrice(trade.expiryPrice, 5) : '--'}
            mono
          />
          <Field label="Entry time" value={formatDateTime(trade.entryTimestamp)} />
          <Field label="Expiry time" value={formatDateTime(trade.expiryTimestamp)} />
          <Field label="Settled at" value={trade.settledAt ? formatDateTime(trade.settledAt) : '--'} />
          <Field label="Result" value={trade.result ?? '--'} />
          <Field
            label="Customer settlement"
            value={trade.settlementAmount !== null ? formatSignedMoney(trade.settlementAmount) : '--'}
          />
          <Field label="Placed" value={formatDateTime(trade.createdAt)} />
        </dl>
      </Panel>

      {open && can('trades:cancel') ? (
        <Panel>
          <PanelHeader
            title="Cancel this trade"
            description="Only an open trade can be cancelled. The stake is returned in full and nothing is won or lost."
          />
          <form
            className="flex flex-wrap gap-3 p-4"
            onSubmit={(event) => {
              event.preventDefault();
              if (reason.trim().length < 3) {
                toast('error', 'A reason is required');
                return;
              }
              cancelMutation.mutate();
            }}
          >
            <input
              className="field flex-1"
              placeholder="Reason for cancellation"
              value={reason}
              onChange={(event) => setReason(event.target.value)}
            />
            <button type="submit" className="btn-danger" disabled={cancelMutation.isPending}>
              Cancel trade
            </button>
          </form>
        </Panel>
      ) : null}
    </div>
  );
}

function Field({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="bg-surface-900 px-4 py-3">
      <dt className="text-[10px] uppercase tracking-wider text-content-400">{label}</dt>
      <dd className={'mt-0.5 truncate text-sm font-medium text-content-100 tnum ' + (mono ? 'font-mono text-xs' : '')}>
        {value}
      </dd>
    </div>
  );
}
