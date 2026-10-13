import { Link, useParams } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ArrowLeft } from 'lucide-react';
import { api } from '../services/api';
import { ApiRequestError } from '../services/http';
import { useAuthStore } from '../stores/authStore';
import { toast } from '../hooks/useToast';
import { formatDateTime, formatExpiry, fromMinor, toMinor } from '../utils/format';
import { Badge, Panel, PanelHeader, Skeleton, StatusBadge } from '../components/ui';

/**
 * One asset: risk parameters and its versioned payout table.
 *
 * Saving a payout supersedes the current version rather than editing it, so the
 * terms a historical trade was accepted under remain readable forever.
 */
export function AssetDetailPage() {
  const { id = '' } = useParams();
  const queryClient = useQueryClient();
  const can = useAuthStore((s) => s.can);

  const query = useQuery({ queryKey: ['assets', id], queryFn: () => api.assets.detail(id) });

  const updateMutation = useMutation({
    mutationFn: (input: Record<string, unknown>) => api.assets.update(id, input),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['assets'] });
      toast('success', 'Asset updated');
    },
    onError: (err) => toast('error', 'Update failed', err instanceof ApiRequestError ? err.message : undefined),
  });

  const payoutMutation = useMutation({
    mutationFn: (input: { expirySeconds: number; payoutPercent: number; note?: string }) =>
      api.assets.setPayout(id, input),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['assets'] });
      toast('success', 'Payout version created', 'Existing trades keep their original terms');
    },
    onError: (err) => toast('error', 'Payout update failed', err instanceof ApiRequestError ? err.message : undefined),
  });

  if (query.isLoading || !query.data) return <Skeleton className="h-96 w-full" />;
  const asset = query.data;
  const writable = can('assets:write');

  return (
    <div className="space-y-4">
      <Link to="/admin/assets" className="inline-flex items-center gap-1.5 text-sm text-content-400 hover:text-content-100">
        <ArrowLeft className="h-4 w-4" />
        Back to assets
      </Link>

      <Panel className="flex flex-wrap items-center gap-4 p-4">
        <div className="min-w-0">
          <h1 className="text-lg font-semibold text-content-100">{asset.symbol}</h1>
          <p className="text-sm text-content-400">
            {asset.name} · {asset.provider}/{asset.providerSymbol}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Badge tone="neutral">{asset.category}</Badge>
          <StatusBadge status={asset.active ? 'ACTIVE' : 'DISABLED'} />
          <StatusBadge status={asset.marketOpen ? 'OPEN' : 'CLOSED'} />
        </div>
      </Panel>

      <Panel>
        <PanelHeader title="Risk parameters" description="Applied to every new trade on this asset" />
        <form
          className="grid gap-4 p-4 sm:grid-cols-2 xl:grid-cols-4"
          onSubmit={(event) => {
            event.preventDefault();
            const data = new FormData(event.currentTarget);
            updateMutation.mutate({
              minStake: toMinor(Number(data.get('minStake'))),
              maxStake: toMinor(Number(data.get('maxStake'))),
              exposureLimit: toMinor(Number(data.get('exposureLimit'))),
              netExposureLimit: toMinor(Number(data.get('netExposureLimit'))),
              hedgeEnabled: data.get('hedgeEnabled') === 'on',
              targetHedgeRatio: Number(data.get('targetHedgeRatio')),
              minHedgeNotional: toMinor(Number(data.get('minHedgeNotional'))),
              maxHedgeNotional: toMinor(Number(data.get('maxHedgeNotional'))),
            });
          }}
        >
          <NumberField name="minStake" label="Min stake" defaultValue={fromMinor(asset.minStake)} disabled={!writable} />
          <NumberField name="maxStake" label="Max stake" defaultValue={fromMinor(asset.maxStake)} disabled={!writable} />
          <NumberField
            name="exposureLimit"
            label="Gross exposure limit"
            defaultValue={fromMinor(asset.exposureLimit)}
            disabled={!writable}
          />
          <NumberField
            name="netExposureLimit"
            label="Net exposure limit"
            defaultValue={fromMinor(asset.netExposureLimit)}
            disabled={!writable}
          />
          <NumberField
            name="targetHedgeRatio"
            label="Target hedge ratio"
            step="0.05"
            defaultValue={asset.targetHedgeRatio}
            disabled={!writable}
          />
          <NumberField
            name="minHedgeNotional"
            label="Min hedge size"
            defaultValue={fromMinor(asset.minHedgeNotional)}
            disabled={!writable}
          />
          <NumberField
            name="maxHedgeNotional"
            label="Max hedge size"
            defaultValue={fromMinor(asset.maxHedgeNotional)}
            disabled={!writable}
          />
          <div className="flex flex-col justify-end gap-3">
            <label className="flex items-center gap-2 text-sm text-content-200">
              <input type="checkbox" name="hedgeEnabled" defaultChecked={asset.hedgeEnabled} disabled={!writable} />
              Hedging enabled
            </label>
            {writable ? (
              <button type="submit" className="btn-primary" disabled={updateMutation.isPending}>
                Save parameters
              </button>
            ) : null}
          </div>
        </form>
      </Panel>

      <div className="grid gap-3 lg:grid-cols-2">
        <Panel className="overflow-hidden">
          <PanelHeader title="Active payouts" description="One active version per expiry" />
          <div className="overflow-x-auto">
            <table className="table">
              <thead>
                <tr>
                  <th>Expiry</th>
                  <th className="text-right">Payout</th>
                  <th className="text-right">Version</th>
                  <th>Effective from</th>
                </tr>
              </thead>
              <tbody>
                {asset.payouts.map((payout) => (
                  <tr key={payout.id}>
                    <td className="font-medium text-content-100">{formatExpiry(payout.expirySeconds)}</td>
                    <td className="text-right font-semibold tnum text-up">{payout.payoutPercent}%</td>
                    <td className="text-right tnum text-content-400">v{payout.version}</td>
                    <td className="text-[11px] text-content-400">{formatDateTime(payout.effectiveFrom)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          {can('payouts:write') ? (
            <form
              className="grid gap-3 border-t border-surface-800 p-4 sm:grid-cols-[140px_140px_minmax(0,1fr)_auto]"
              onSubmit={(event) => {
                event.preventDefault();
                const data = new FormData(event.currentTarget);
                payoutMutation.mutate({
                  expirySeconds: Number(data.get('expirySeconds')),
                  payoutPercent: Number(data.get('payoutPercent')),
                  note: String(data.get('note') ?? '') || undefined,
                });
                event.currentTarget.reset();
              }}
            >
              <div>
                <label className="label" htmlFor="expirySeconds">
                  Expiry
                </label>
                <select id="expirySeconds" name="expirySeconds" className="field">
                  {asset.supportedExpiries.map((seconds) => (
                    <option key={seconds} value={seconds}>
                      {formatExpiry(seconds)}
                    </option>
                  ))}
                </select>
              </div>
              <div>
                <label className="label" htmlFor="payoutPercent">
                  Payout %
                </label>
                <input
                  id="payoutPercent"
                  name="payoutPercent"
                  type="number"
                  step="0.5"
                  min="0"
                  max="500"
                  required
                  className="field tnum"
                  defaultValue={82}
                />
              </div>
              <div>
                <label className="label" htmlFor="note">
                  Note
                </label>
                <input id="note" name="note" className="field" placeholder="Reason for the change" />
              </div>
              <div className="flex items-end">
                <button type="submit" className="btn-primary" disabled={payoutMutation.isPending}>
                  Publish
                </button>
              </div>
            </form>
          ) : null}
        </Panel>

        <Panel className="overflow-hidden">
          <PanelHeader title="Payout history" description="Superseded versions are kept for audit" />
          <div className="max-h-[420px] overflow-y-auto">
            <table className="table">
              <thead>
                <tr>
                  <th>Expiry</th>
                  <th className="text-right">Payout</th>
                  <th className="text-right">Version</th>
                  <th>Window</th>
                  <th>Note</th>
                </tr>
              </thead>
              <tbody>
                {(asset.payoutHistory ?? []).map((payout) => (
                  <tr key={payout.id} className={payout.active ? '' : 'opacity-60'}>
                    <td className="text-content-200">{formatExpiry(payout.expirySeconds)}</td>
                    <td className="text-right tnum text-content-200">{payout.payoutPercent}%</td>
                    <td className="text-right tnum text-content-400">v{payout.version}</td>
                    <td className="text-[11px] text-content-400">
                      {formatDateTime(payout.effectiveFrom)}
                      {payout.effectiveTo ? ' to ' + formatDateTime(payout.effectiveTo) : ' (active)'}
                    </td>
                    <td className="text-[11px] text-content-400">{payout.note ?? ''}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Panel>
      </div>

      <p className="px-1 text-[11px] text-content-400">
        Stake bounds and exposure limits apply to new trades only. Changing a payout creates a new version; open and
        historical trades keep the payout that was active when they were accepted.
      </p>
    </div>
  );
}

function NumberField({
  name,
  label,
  defaultValue,
  step,
  disabled,
}: {
  name: string;
  label: string;
  defaultValue: number;
  step?: string;
  disabled?: boolean;
}) {
  return (
    <div>
      <label className="label" htmlFor={name}>
        {label}
      </label>
      <input
        id={name}
        name={name}
        type="number"
        step={step ?? '1'}
        defaultValue={defaultValue}
        disabled={disabled}
        className="field tnum"
      />
    </div>
  );
}
