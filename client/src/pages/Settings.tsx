import { useQuery } from '@tanstack/react-query';
import { api } from '../services/api';
import { useSettingsStore } from '../stores/settingsStore';
import { formatExpiry, formatMoney, fromMinor, toMinor } from '../utils/format';
import { FeedBadge, Panel, Skeleton } from '../components/ui';
import { ThemeToggle } from '../components/ThemeToggle';
import { useThemeStore } from '../stores/themeStore';
import { OneTapSwitch, TradeModeControl } from '../components/TradingPreferences';

/** Trading preferences plus a live view of platform feed health. */
export function SettingsPage() {
  const settings = useSettingsStore();
  const themePreference = useThemeStore((s) => s.preference);
  const resolvedTheme = useThemeStore((s) => s.resolved);
  const healthQuery = useQuery({
    queryKey: ['market', 'health'],
    queryFn: api.market.health,
    refetchInterval: 3000,
  });

  return (
    <div className="mx-auto max-w-2xl space-y-3">
      <Panel className="p-5">
        <h1 className="text-sm font-semibold text-content-100">Appearance</h1>
        <div className="mt-4 flex flex-wrap items-center justify-between gap-3 rounded-lg border border-surface-700 bg-surface-850 px-4 py-3">
          <div>
            <div className="text-sm font-medium text-content-100">Colour theme</div>
            <div className="text-[11px] text-content-400">
              {themePreference === 'system'
                ? 'Following your device, currently ' + resolvedTheme
                : 'Always ' + themePreference}
            </div>
          </div>
          <ThemeToggle size="md" />
        </div>
      </Panel>

      <Panel className="p-5">
        <h1 className="text-sm font-semibold text-content-100">Trading preferences</h1>

        <div className="mt-4 space-y-4">
          <div>
            <label className="label" htmlFor="defaultStake">
              Default stake
            </label>
            <input
              id="defaultStake"
              inputMode="decimal"
              className="field tnum"
              value={fromMinor(settings.defaultStake)}
              onChange={(event) => {
                const parsed = Number(event.target.value.replace(/[^0-9.]/g, ''));
                if (Number.isFinite(parsed)) settings.setDefaultStake(toMinor(parsed));
              }}
            />
            <p className="mt-1 text-[11px] text-content-400">
              Pre-filled on the trade ticket: {formatMoney(settings.defaultStake)}
            </p>
          </div>

          <div>
            <span className="label">Default expiry</span>
            <div className="flex flex-wrap gap-1">
              {[30, 60, 300].map((seconds) => (
                <button
                  key={seconds}
                  onClick={() => settings.setDefaultExpiry(seconds)}
                  className={
                    'rounded-lg border px-3 py-1.5 text-sm font-medium transition ' +
                    (settings.defaultExpirySeconds === seconds
                      ? 'border-accent bg-accent/15 text-content-100'
                      : 'border-surface-700 bg-surface-850 text-content-300')
                  }
                >
                  {formatExpiry(seconds)}
                </button>
              ))}
            </div>
          </div>

          <Toggle
            label="Sound on settlement"
            description="Play a short cue when a trade settles."
            checked={settings.soundEnabled}
            onChange={settings.toggleSound}
          />
          <OneTapSwitch />
          <TradeModeControl />
        </div>
      </Panel>

      <Panel className="overflow-hidden">
        <div className="border-b border-surface-800 px-5 py-3">
          <h2 className="text-sm font-semibold text-content-100">Market data health</h2>
          <p className="text-[11px] text-content-400">
            When a feed is stale or offline, new trades on that asset are paused by the platform.
          </p>
        </div>

        {healthQuery.isLoading ? (
          <div className="space-y-2 p-4">
            {Array.from({ length: 4 }).map((_, index) => (
              <Skeleton key={index} className="h-10 w-full" />
            ))}
          </div>
        ) : (
          <ul className="divide-y divide-surface-800/70">
            {(healthQuery.data ?? []).map((feed) => (
              <li key={feed.assetId} className="flex items-center gap-3 px-5 py-2.5">
                <span className="flex-1 text-sm font-medium text-content-100">{feed.symbol}</span>
                <span className="text-xs text-content-400 tnum">
                  {feed.lastPrice !== null ? feed.lastPrice : '--'}
                </span>
                <FeedBadge status={feed.status} latencyMs={feed.totalLatencyMs} />
              </li>
            ))}
          </ul>
        )}
      </Panel>
    </div>
  );
}

function Toggle({
  label,
  description,
  checked,
  onChange,
}: {
  label: string;
  description: string;
  checked: boolean;
  onChange: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onChange}
      className="flex w-full items-center gap-4 rounded-lg border border-surface-700 bg-surface-850 px-4 py-3 text-left"
      role="switch"
      aria-checked={checked}
    >
      <span className="min-w-0 flex-1">
        <span className="block text-sm font-medium text-content-100">{label}</span>
        <span className="block text-[11px] text-content-400">{description}</span>
      </span>
      <span
        className={
          'relative h-6 w-11 shrink-0 rounded-full transition ' + (checked ? 'bg-accent' : 'bg-surface-700')
        }
      >
        <span
          className={
            'absolute top-0.5 h-5 w-5 rounded-full shadow-panel transition-all ' +
          (checked ? 'left-[22px] bg-white' : 'left-0.5 bg-surface-500')
          }
        />
      </span>
    </button>
  );
}
