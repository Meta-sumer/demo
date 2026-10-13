import clsx from 'clsx';
import { Plus, X } from 'lucide-react';
import { useMarketStore } from '../../stores/marketStore';
import { useWorkspaceStore } from '../../stores/workspaceStore';
import { formatPrice } from '../../utils/format';
import { FeedBadge } from '../../components/ui';
import type { Asset } from '../../types/api';

/**
 * Open instruments, as tabs.
 *
 * Each tab carries the one number that decides whether a trader looks further -
 * the payout - so switching is a decision they can make without opening the
 * instrument first.
 *
 * Rendered inside the top bar, so this owns no bar of its own - no background,
 * no border, no row padding. It is a run of controls that sits in whatever
 * gives it room, and it shrinks before the bar does. The chart layout control
 * ends the run on wide screens.
 */
export function AssetTabs({ assets }: { assets: Asset[] }) {
  const openAssetIds = useWorkspaceStore((s) => s.openAssetIds);
  const activeAssetId = useWorkspaceStore((s) => s.activeAssetId);
  const setActive = useWorkspaceStore((s) => s.setActive);
  const closeAsset = useWorkspaceStore((s) => s.closeAsset);
  const setPickerOpen = useWorkspaceStore((s) => s.setAssetPickerOpen);
  const ticks = useMarketStore((s) => s.ticks);

  const byId = new Map(assets.map((asset) => [asset.id, asset]));
  const openAssets = openAssetIds.map((id) => byId.get(id)).filter((a): a is Asset => !!a);
  const activeAsset = byId.get(activeAssetId ?? '') ?? openAssets[0] ?? null;

  return (
    <div className="flex min-w-0 flex-1 items-center gap-1.5">
      <button
        type="button"
        onClick={() => setPickerOpen(true)}
        className="grid h-9 w-9 shrink-0 place-items-center rounded-lg bg-accent text-surface-950 transition hover:brightness-110"
        aria-label="Add an instrument"
        title="Add an instrument"
      >
        <Plus className="h-[18px] w-[18px]" />
      </button>

      <div className="flex min-w-0 items-center gap-1.5 overflow-x-auto">
        {openAssets.map((asset) => {
          const active = asset.id === activeAssetId;
          const bestPayout = asset.payouts.reduce((best, p) => Math.max(best, p.payoutPercent), 0);
          const price = ticks[asset.id]?.price ?? asset.feed.lastPrice;
          const tradable = asset.marketOpen && asset.feed.status !== 'OFFLINE' && asset.feed.status !== 'STALE';

          return (
            <div
              key={asset.id}
              className={clsx(
                'group relative flex shrink-0 items-center gap-2 rounded-lg border px-2.5 py-1 transition',
                active
                  ? 'border-accent/50 bg-surface-850 shadow-raised'
                  : 'border-surface-700 bg-surface-900 hover:border-surface-600',
              )}
            >
              <button
                type="button"
                onClick={() => setActive(asset.id)}
                className="flex items-center gap-2 text-left"
                aria-current={active ? 'true' : undefined}
              >
                <span
                  className={clsx('h-1.5 w-1.5 shrink-0 rounded-full', tradable ? 'bg-up' : 'bg-down')}
                  title={tradable ? 'Tradable' : 'Paused'}
                />
                <span>
                  <span className="block text-[11px] font-semibold leading-tight text-content-100">{asset.symbol}</span>
                  <span className="block text-[10px] font-bold leading-tight text-warn">{bestPayout}%</span>
                </span>
                <span className="hidden text-[11px] tnum text-content-300 sm:block">
                  {price !== null ? formatPrice(price, asset.pricePrecision) : '--'}
                </span>
              </button>

              {openAssets.length > 1 ? (
                <button
                  type="button"
                  onClick={() => closeAsset(asset.id)}
                  className="rounded-md p-0.5 text-content-400 opacity-0 transition hover:text-down focus-visible:opacity-100 group-hover:opacity-100"
                  aria-label={'Close ' + asset.symbol}
                >
                  <X className="h-3 w-3" />
                </button>
              ) : null}
            </div>
          );
        })}
      </div>

      {/* Feed detail for the instrument in view - the tab dot says tradable or
          not, this says why. */}
      {/* Reports the active instrument's feed, so it belongs beside the tabs
          rather than out at the edge. First thing dropped when the bar is
          tight - the trade ticket refuses the order anyway if the feed is
          not healthy. */}
      {activeAsset ? (
        <span className="hidden shrink-0 lg:block">
          <FeedBadge status={activeAsset.feed.status} latencyMs={activeAsset.feed.totalLatencyMs} />
        </span>
      ) : null}

      <ChartLayoutToggle canSplit={openAssets.length > 1} />
    </div>
  );
}

/**
 * One chart or two side by side - wide screens, advanced mode. It lives here,
 * with the tabs the second chart is chosen from, rather than in a row of its
 * own over the chart: that row appeared only in advanced mode, so switching
 * mode moved the chart and the mode toggle. Two charts need a second open tab.
 */
function ChartLayoutToggle({ canSplit }: { canSplit: boolean }) {
  const layout = useWorkspaceStore((s) => s.chartLayout);
  const setLayout = useWorkspaceStore((s) => s.setChartLayout);
  const shown = canSplit ? layout : 1;

  return (
    <div
      role="group"
      aria-label="Chart layout"
      className="ml-auto hidden shrink-0 rounded-lg bg-surface-850 p-0.5 text-[11px] font-semibold xl:flex"
    >
      {([1, 2] as const).map((count) => {
        const unavailable = count === 2 && !canSplit;
        return (
          <button
            key={count}
            type="button"
            onClick={() => setLayout(count)}
            disabled={unavailable}
            aria-pressed={shown === count}
            title={unavailable ? 'Open another asset tab to compare' : undefined}
            className={clsx(
              'rounded-md px-2 py-1 transition disabled:cursor-not-allowed disabled:opacity-40',
              shown === count ? 'bg-surface-700 text-content-100' : 'text-content-400 hover:text-content-200',
            )}
          >
            {count === 1 ? '1 chart' : '2 charts'}
          </button>
        );
      })}
    </div>
  );
}
