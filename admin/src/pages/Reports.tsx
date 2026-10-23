import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '../services/api';
import { formatDateTime, formatMoney, formatSignedMoney } from '../utils/format';
import { Badge, Panel, PanelHeader, Skeleton, Stat } from '../components/ui';
import { StaleBadge, Unavailable } from '../components/freshness';

/**
 * Platform P and L (spec section 41).
 *
 * The point of this screen is that the result is decomposed rather than netted:
 * customer payouts, book result, hedge legs, fees and adjustments are shown
 * separately so the total can be reconciled against the ledgers.
 */
export function ReportsPage() {
  const today = new Date().toISOString().slice(0, 10);
  const monthAgo = new Date(Date.now() - 30 * 86_400_000).toISOString().slice(0, 10);
  const [range, setRange] = useState({ from: monthAgo, to: today });

  const query = useQuery({
    queryKey: ['reports', 'pnl', range],
    queryFn: () => api.reports.pnl({ from: range.from, to: range.to + 'T23:59:59.999Z' }),
  });

  const pnl = query.data;
  // A figure that could not be loaded reads "unavailable", never 0.
  const placeholder = query.isError ? <Unavailable /> : <Skeleton className="h-6 w-24" />;

  return (
    <div className="space-y-4">
      <Panel className="flex flex-wrap items-end gap-3 p-4">
        <div>
          <label className="label" htmlFor="from">
            From
          </label>
          <input
            id="from"
            type="date"
            className="field w-44"
            value={range.from}
            onChange={(event) => setRange((r) => ({ ...r, from: event.target.value }))}
          />
        </div>
        <div>
          <label className="label" htmlFor="to">
            To
          </label>
          <input
            id="to"
            type="date"
            className="field w-44"
            value={range.to}
            onChange={(event) => setRange((r) => ({ ...r, to: event.target.value }))}
          />
        </div>
      </Panel>

      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <Stat
          label="Net result"
          value={pnl ? formatSignedMoney(pnl.netResult) : placeholder}
          tone={pnl ? (pnl.netResult >= 0 ? 'up' : 'down') : 'neutral'}
          hint="Book plus hedge plus fees plus adjustments"
        />
        <Stat
          label="Platform gross result"
          value={pnl ? formatSignedMoney(pnl.platformGrossResult) : placeholder}
          tone={pnl ? (pnl.platformGrossResult >= 0 ? 'up' : 'down') : 'neutral'}
        />
        <Stat
          label="Customer payout"
          value={pnl ? formatMoney(pnl.customerPayout) : placeholder}
          hint={pnl ? 'Stakes collected ' + formatMoney(pnl.customerStakesCollected) : undefined}
        />
        <Stat
          label="Open customer liability"
          value={pnl ? formatMoney(pnl.customerLiability) : placeholder}
          tone="warn"
          hint="Worst case payout on trades still open"
        />
      </div>

      <Panel className="overflow-hidden">
        <PanelHeader title="Result decomposition" description="Each line comes from a different ledger" />
        {query.isError && !pnl ? (
          <div className="p-4">
            <Unavailable />
          </div>
        ) : query.isLoading || !pnl ? (
          <Skeleton className="m-4 h-48" />
        ) : (
          <table className="table">
            <tbody>
              <Row label="Customer stakes collected" value={pnl.customerStakesCollected} />
              <Row label="Customer payouts" value={-pnl.customerPayout} />
              <Row label="Platform gross result (book)" value={pnl.platformGrossResult} emphasis />
              <Row label="Hedge realised result" value={pnl.hedgeRealisedPnl} />
              {pnl.hedgeTradingPnl !== undefined ? (
                <>
                  <Row label="Hedge: trading result (average cost)" value={pnl.hedgeTradingPnl} muted />
                  <Row label="Hedge: fees" value={-(pnl.hedgeFees ?? 0)} muted />
                  <Row label="Hedge: funding" value={pnl.hedgeFunding ?? 0} muted />
                  <Row label="Hedge: corrections" value={pnl.hedgeAdjustments ?? 0} muted />
                </>
              ) : null}
              <Row label="Hedge unrealised result" value={pnl.hedgeUnrealisedPnl} muted />
              <Row label="Fees" value={pnl.fees} />
              <Row label="Adjustments" value={pnl.adjustments} />
              <Row label="Net result" value={pnl.netResult} emphasis />
            </tbody>
          </table>
        )}
        <p className="border-t border-surface-800 px-4 py-3 text-[11px] text-content-400">
          Platform result is never computed as wins minus losses. Unrealised hedge result is shown for information and
          is not part of the net figure. Hedge figures come from the venue fills ledger, converted at the INR rate stored
          on each fill.
          {pnl && pnl.hedgeReconcileStatus ? (
            <>
              {' '}Hedge reconciliation:{' '}
              <Badge tone={pnl.hedgeReconcileStatus === 'OK' || pnl.hedgeReconcileStatus === 'NOT_APPLICABLE' ? 'up' : pnl.hedgeReconcileStatus === 'DRIFT' ? 'down' : 'warn'}>
                {pnl.hedgeReconcileStatus}
              </Badge>
              {pnl.hedgeReconciledAt ? ' at ' + formatDateTime(pnl.hedgeReconciledAt) : ''}
              {pnl.hedgeUnrealisedPnl !== 0 ? <StaleBadge asOf={pnl.hedgeMarkedAt} maxAgeMs={60_000} label="hedge mark" /> : null}
            </>
          ) : null}
          {pnl?.generatedAt ? ' Generated ' + formatDateTime(pnl.generatedAt) + '.' : null}
        </p>
      </Panel>
    </div>
  );
}

function Row({
  label,
  value,
  emphasis,
  muted,
}: {
  label: string;
  value: number;
  emphasis?: boolean;
  muted?: boolean;
}) {
  return (
    <tr>
      <td className={emphasis ? 'font-semibold text-content-100' : muted ? 'text-content-400' : 'text-content-200'}>{label}</td>
      <td
        className={
          'text-right tnum ' +
          (emphasis ? 'font-bold ' : '') +
          (value > 0 ? 'text-up' : value < 0 ? 'text-down' : 'text-content-300')
        }
      >
        {formatSignedMoney(value)}
      </td>
    </tr>
  );
}
