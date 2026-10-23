import { useState } from 'react';
import { Link } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../services/api';
import { ApiRequestError } from '../services/http';
import { useAuthStore } from '../stores/authStore';
import { toast } from '../hooks/useToast';
import { formatExpiry, formatMoney } from '../utils/format';
import { Badge, Panel, PanelHeader, StatusBadge, TableSkeleton } from '../components/ui';

/** Asset catalogue (spec section 25). */
export function AssetsPage() {
  const queryClient = useQueryClient();
  const can = useAuthStore((s) => s.can);
  const [creating, setCreating] = useState(false);

  const query = useQuery({ queryKey: ['assets'], queryFn: api.assets.list });

  const toggleMutation = useMutation({
    mutationFn: ({ id, active }: { id: string; active: boolean }) => api.assets.update(id, { active }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['assets'] });
      toast('success', 'Asset updated');
    },
    onError: (err) => toast('error', 'Update failed', err instanceof ApiRequestError ? err.message : undefined),
  });

  const createMutation = useMutation({
    mutationFn: (input: Record<string, unknown>) => api.assets.create(input),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['assets'] });
      setCreating(false);
      toast('success', 'Asset created');
    },
    onError: (err) => toast('error', 'Create failed', err instanceof ApiRequestError ? err.message : undefined),
  });

  return (
    <Panel className="overflow-hidden">
      <PanelHeader
        title="Assets"
        description="Instruments, stake bounds, exposure limits and hedge configuration"
        actions={
          can('assets:write') ? (
            <button className="btn-primary" onClick={() => setCreating((open) => !open)}>
              {creating ? 'Cancel' : 'New asset'}
            </button>
          ) : null
        }
      />

      {creating ? (
        <form
          className="grid gap-3 border-b border-surface-800 bg-surface-850/60 p-4 sm:grid-cols-4"
          onSubmit={(event) => {
            event.preventDefault();
            const data = new FormData(event.currentTarget);
            createMutation.mutate({
              symbol: String(data.get('symbol')),
              name: String(data.get('name')),
              category: String(data.get('category')),
              providerSymbol: String(data.get('providerSymbol')),
              supportedExpiries: String(data.get('expiries'))
                .split(',')
                .map((value) => Number(value.trim()))
                .filter((value) => Number.isFinite(value) && value > 0),
            });
          }}
        >
          <div>
            <label className="label" htmlFor="symbol">
              Symbol
            </label>
            <input id="symbol" name="symbol" required className="field" placeholder="SOL/USD" />
          </div>
          <div>
            <label className="label" htmlFor="name">
              Name
            </label>
            <input id="name" name="name" required className="field" placeholder="Solana / US Dollar" />
          </div>
          <div>
            <label className="label" htmlFor="category">
              Category
            </label>
            <select id="category" name="category" className="field" defaultValue="CRYPTO">
              <option value="CRYPTO">Crypto</option>
              <option value="FOREX">Forex</option>
              <option value="STOCK">Stock</option>
              <option value="COMMODITY">Commodity</option>
              <option value="INDEX">Index</option>
            </select>
          </div>
          <div>
            <label className="label" htmlFor="providerSymbol">
              Provider symbol
            </label>
            <input id="providerSymbol" name="providerSymbol" required className="field" placeholder="solusdt" />
          </div>
          <div className="sm:col-span-3">
            <label className="label" htmlFor="expiries">
              Supported expiries in seconds, comma separated
            </label>
            <input id="expiries" name="expiries" className="field" defaultValue="30,60,300" />
          </div>
          <div className="flex items-end">
            <button type="submit" className="btn-primary w-full" disabled={createMutation.isPending}>
              Create
            </button>
          </div>
        </form>
      ) : null}

      {query.isLoading ? (
        <TableSkeleton rows={6} columns={8} />
      ) : (
        <div className="overflow-x-auto">
          <table className="table min-w-[1100px]">
            <thead>
              <tr>
                <th>Asset</th>
                <th>Category</th>
                <th>Status</th>
                <th className="text-right">Min stake</th>
                <th className="text-right">Max stake</th>
                <th className="text-right">Exposure limit</th>
                <th>Hedging</th>
                <th>Payouts</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {(query.data ?? []).map((asset) => (
                <tr key={asset.id}>
                  <td>
                    <Link to={'/admin/assets/' + asset.id} className="font-medium text-content-100 hover:text-accent">
                      {asset.symbol}
                    </Link>
                    <div className="text-[11px] text-content-400">{asset.providerSymbol}</div>
                  </td>
                  <td className="text-content-300">{asset.category}</td>
                  <td>
                    <StatusBadge status={asset.active ? 'ACTIVE' : 'DISABLED'} />
                    {asset.priced === false ? (
                      <span className="ml-1" title={asset.notPricedReason ?? ''}>
                        <Badge tone="warn">Not priced</Badge>
                      </span>
                    ) : null}
                  </td>
                  <td className="text-right tnum text-content-300">{formatMoney(asset.minStake)}</td>
                  <td className="text-right tnum text-content-300">{formatMoney(asset.maxStake)}</td>
                  <td className="text-right tnum text-content-300">{formatMoney(asset.exposureLimit)}</td>
                  <td>
                    {asset.hedgeEnabled ? (
                      <Badge tone="accent">ratio {asset.targetHedgeRatio}</Badge>
                    ) : (
                      <Badge tone="neutral">off</Badge>
                    )}
                  </td>
                  <td>
                    <div className="flex flex-wrap gap-1">
                      {asset.payouts.map((payout) => (
                        <span key={payout.id} className="rounded bg-surface-800 px-1.5 py-0.5 text-[10px] text-content-200">
                          {formatExpiry(payout.expirySeconds)} {payout.payoutPercent}%
                        </span>
                      ))}
                    </div>
                  </td>
                  <td className="text-right">
                    {can('assets:write') ? (
                      <button
                        className="btn-ghost px-2 py-1 text-[11px]"
                        onClick={() => toggleMutation.mutate({ id: asset.id, active: !asset.active })}
                      >
                        {asset.active ? 'Disable' : 'Enable'}
                      </button>
                    ) : null}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Panel>
  );
}
