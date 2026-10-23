import { useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ArrowLeft } from 'lucide-react';
import { api } from '../services/api';
import { ApiRequestError } from '../services/http';
import { useAuthStore } from '../stores/authStore';
import { toast } from '../hooks/useToast';
import { formatDateTime, formatMoney, formatPrice, formatSignedMoney } from '../utils/format';
import { isVoided, VOID_LABEL } from '../utils/tradeStatus';
import { Badge, Panel, PanelHeader, Skeleton, StatusBadge } from '../components/ui';
import type { PricingComponents } from '../types/api';

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
            {isVoided(trade) ? <Badge tone="warn">{VOID_LABEL}</Badge> : <StatusBadge status={trade.status} />}
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
          <Field label="Result" value={isVoided(trade) ? 'Void · stake refunded' : (trade.result ?? '--')} />
          {isVoided(trade) ? <Field label="Why voided" value={trade.voidReason ?? 'No valid market price at expiry'} /> : null}
          <Field label="Settlement rule" value={trade.expiryPriceSource ?? '--'} mono />
          <Field label="Tie rule (frozen)" value={trade.drawPolicy ?? 'policy at settlement'} />
          <Field
            label="Customer settlement"
            value={trade.settlementAmount !== null ? formatSignedMoney(trade.settlementAmount) : '--'}
          />
          <Field label="Placed" value={formatDateTime(trade.createdAt)} />
        </dl>
      </Panel>

      {trade.pricing ? (
        <Panel className="overflow-hidden">
          <PanelHeader
            title="Pricing"
            description={
              trade.pricing.inline
                ? 'Priced at acceptance (the app sent no offer)'
                : 'Accepted on offer ' + trade.pricing.offerId
            }
          />
          <dl className="grid grid-cols-2 gap-px bg-surface-800/60 sm:grid-cols-4">
            <Field label="Rule version" value={trade.pricing.ruleVersion} mono />
            <Field label="Mode" value={trade.pricing.mode} />
            <Field label="Locked payout" value={trade.pricing.payoutPercent + '%'} />
            <Field
              label="Displayed payout"
              value={trade.pricing.displayedPayoutPercent !== null ? trade.pricing.displayedPayoutPercent + '%' : '--'}
            />
            <Field label="Offer reference" value={formatPrice(trade.pricing.referencePrice, 5)} mono />
            <Field label="Mid at acceptance" value={formatPrice(trade.pricing.acceptMid, 5)} mono />
            <Field label="Slippage" value={trade.pricing.slippageBps.toFixed(3) + ' bps'} />
            <Field label="Feed" value={trade.pricing.feedProvider || '--'} />
            <Field label="Base / min / max" value={terms(trade.pricing.components)} />
            <Field label="Vol ratio" value={trade.pricing.components.volRatio.toFixed(2)} />
            <Field label="Spread" value={trade.pricing.components.spreadBps.toFixed(3) + ' bps'} />
            <Field label="Expiry factor" value={String(trade.pricing.components.expiryFactor)} />
            <Field label="Vol adj" value={points(trade.pricing.components.volAdj)} />
            <Field label="Spread adj" value={points(trade.pricing.components.spreadAdj)} />
            <Field
              label="Hedge adj"
              value={
                points(trade.pricing.components.hedgeAdj + trade.pricing.components.hedgeUnavailableAdj) +
                (trade.pricing.components.hedgeAvailable ? '' : ' (hedge unavailable)')
              }
            />
            <Field label="Exposure adj" value={points(trade.pricing.components.exposureAdj)} />
            <Field label="Margin adj" value={points(trade.pricing.components.marginAdj)} />
            <Field label="Utilisation" value={(trade.pricing.components.utilFinal * 100).toFixed(1) + '%'} />
            <Field
              label="Guard"
              value={
                trade.pricing.guard.enabled
                  ? 'model v' + trade.pricing.guard.modelVersion + ', edge ' + (trade.pricing.guard.pointEdge * 100).toFixed(1) + '%'
                  : 'off'
              }
            />
            <Field label="Guard regime" value={trade.pricing.guard.regime || '--'} />
          </dl>
        </Panel>
      ) : null}

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

/** Payout points of a fraction: 0.035 -> "3.50 pts". */
function points(fraction: number): string {
  return (fraction * 100).toFixed(2) + ' pts';
}

function terms(c: PricingComponents): string {
  return [c.base, c.min, c.max].map((v) => Math.round(v * 1000) / 10 + '%').join(' / ');
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
