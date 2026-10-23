import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../services/api';
import { ApiRequestError } from '../services/http';
import { useAuthStore } from '../stores/authStore';
import { toast } from '../hooks/useToast';
import { formatDateTime, fromMinor, toMinor } from '../utils/format';
import { Badge, Panel, PanelHeader, Skeleton } from '../components/ui';
import { ThemeToggle } from '../components/ThemeToggle';
import { useThemeStore } from '../stores/themeStore';

/**
 * Platform risk configuration (spec sections 13, 15, 17).
 *
 * Saving does not edit the live row: it publishes a new version and supersedes
 * the previous one, so every past risk decision can be replayed against the
 * configuration that produced it. The version history is shown underneath.
 */
export function SettingsPage() {
  const queryClient = useQueryClient();
  const can = useAuthStore((s) => s.can);
  const writable = can('risk:write');
  const [note, setNote] = useState('');
  const themePreference = useThemeStore((s) => s.preference);
  const resolvedTheme = useThemeStore((s) => s.resolved);

  const query = useQuery({ queryKey: ['risk', 'config'], queryFn: api.risk.config });

  const mutation = useMutation({
    mutationFn: (input: Record<string, unknown>) => api.risk.updateConfig(input),
    onSuccess: (config) => {
      void queryClient.invalidateQueries({ queryKey: ['risk'] });
      setNote('');
      toast('success', 'Risk configuration v' + config.version + ' published');
    },
    onError: (err) => toast('error', 'Update failed', err instanceof ApiRequestError ? err.message : undefined),
  });

  if (query.isLoading || !query.data) return <Skeleton className="h-96 w-full" />;
  const { config, versions } = query.data;

  return (
    <div className="space-y-4">
      <Panel>
        <PanelHeader
          title="Appearance"
          description="Stored on this device only - it is not part of the platform configuration"
        />
        <div className="flex flex-wrap items-center justify-between gap-3 p-4">
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

      <Panel>
        <PanelHeader
          title={'Risk configuration (version ' + config.version + ')'}
          description="Every change publishes a new version and is written to the audit log"
          actions={config.tradingHalted ? <Badge tone="critical">TRADING HALTED</Badge> : null}
        />

        <form
          className="space-y-6 p-4"
          onSubmit={(event) => {
            event.preventDefault();
            const data = new FormData(event.currentTarget);
            const number = (key: string) => Number(data.get(key));
            const money = (key: string) => toMinor(Number(data.get(key)));
            // Fields an older server does not send are not rendered, and not sent.
            const optional = (key: string) => (data.has(key) ? Number(data.get(key)) : undefined);

            mutation.mutate({
              maxAssetExposure: money('maxAssetExposure'),
              maxUserExposure: money('maxUserExposure'),
              maxGrossExposure: money('maxGrossExposure'),
              minTradeAmount: money('minTradeAmount'),
              maxTradeAmount: money('maxTradeAmount'),
              maxOpenTradesPerUser: number('maxOpenTradesPerUser'),
              maxUserAssetConcentrationPercent: number('maxUserAssetConcentrationPercent'),
              maxExpiryLiability: data.has('maxExpiryLiability') ? money('maxExpiryLiability') : undefined,
              maxAssetLiability: data.has('maxAssetLiability') ? money('maxAssetLiability') : undefined,
              maxPlatformLiability: data.has('maxPlatformLiability') ? money('maxPlatformLiability') : undefined,
              hedgeThresholdPercent: number('hedgeThresholdPercent'),
              criticalExposurePercent: number('criticalExposurePercent'),
              minSecondsToExpiryForHedge: number('minSecondsToExpiryForHedge'),
              volatilityHedgeBoost: number('volatilityHedgeBoost'),
              drawPolicy: String(data.get('drawPolicy')),
              slippageTolerancePercent: number('slippageTolerancePercent'),
              earlyCloseEnabled: data.get('earlyCloseEnabled') === 'on',
              earlyCloseFeePercent: number('earlyCloseFeePercent'),
              minSecondsRemainingForEarlyClose: number('minSecondsRemainingForEarlyClose'),
              feedHealthyMaxMs: number('feedHealthyMaxMs'),
              feedDegradedMaxMs: number('feedDegradedMaxMs'),
              feedTickMaxAgeMs: number('feedTickMaxAgeMs'),
              feedTickHistoryRetentionMs: optional('feedTickHistoryRetentionMs'),
              feedGapMinMs: optional('feedGapMinMs'),
              feedGapIntervalMultiple: optional('feedGapIntervalMultiple'),
              feedSaturationHoldMs: optional('feedSaturationHoldMs'),
              feedMaxClockDriftMs: optional('feedMaxClockDriftMs'),
              feedClockCheckIntervalMs: optional('feedClockCheckIntervalMs'),
              settlementMaxTickAgeMs: optional('settlementMaxTickAgeMs'),
              settlementConfirmationWindowMs: optional('settlementConfirmationWindowMs'),
              settlementDeferWindowMs: optional('settlementDeferWindowMs'),
              settlementRetryDelayMs: optional('settlementRetryDelayMs'),
              settlementMaxClockSkewMs: optional('settlementMaxClockSkewMs'),
              settlementNoPriceRule: data.has('settlementNoPriceRule') ? String(data.get('settlementNoPriceRule')) : undefined,
              earlyCloseFloorMs: optional('earlyCloseFloorMs'),
              earlyCloseMaxTickAgeMs: optional('earlyCloseMaxTickAgeMs'),
              earlyCloseQuoteMaxAgeMs: optional('earlyCloseQuoteMaxAgeMs'),
              earlyCloseQuoteTolerancePercent: optional('earlyCloseQuoteTolerancePercent'),
              haltTradingOnStaleFeed: data.get('haltTradingOnStaleFeed') === 'on',
              haltTradingOnDegradedFeed: data.get('haltTradingOnDegradedFeed') === 'on',
              tradingHalted: data.get('tradingHalted') === 'on',
              note: note || undefined,
            });
          }}
        >
          <Section title="Exposure ceilings">
            <Money
              name="maxAssetExposure"
              label="Max exposure per asset"
              value={config.maxAssetExposure}
              disabled={!writable}
            />
            <Money
              name="maxUserExposure"
              label="Max exposure per user"
              value={config.maxUserExposure}
              disabled={!writable}
            />
            <Money
              name="maxGrossExposure"
              label="Max platform gross exposure"
              value={config.maxGrossExposure}
              disabled={!writable}
            />
            <Num
              name="maxUserAssetConcentrationPercent"
              label="Max user share of one book (%)"
              value={config.maxUserAssetConcentrationPercent}
              disabled={!writable}
            />
            {config.maxExpiryLiability !== undefined ? (
              <>
                <Money
                  name="maxExpiryLiability"
                  label="Max liability per expiry (0 = off)"
                  value={config.maxExpiryLiability}
                  disabled={!writable}
                />
                <Money
                  name="maxAssetLiability"
                  label="Max liability per asset (0 = off)"
                  value={config.maxAssetLiability ?? 0}
                  disabled={!writable}
                />
                <Money
                  name="maxPlatformLiability"
                  label="Max platform liability (0 = off)"
                  value={config.maxPlatformLiability ?? 0}
                  disabled={!writable}
                />
              </>
            ) : null}
          </Section>

          <Section title="Trade limits">
            <Money
              name="minTradeAmount"
              label="Minimum stake"
              value={config.minTradeAmount}
              disabled={!writable}
            />
            <Money
              name="maxTradeAmount"
              label="Maximum stake"
              value={config.maxTradeAmount}
              disabled={!writable}
            />
            <Num
              name="maxOpenTradesPerUser"
              label="Max open trades per user"
              value={config.maxOpenTradesPerUser}
              disabled={!writable}
            />
            <div>
              <label className="label" htmlFor="drawPolicy">
                Draw policy
              </label>
              <select
                id="drawPolicy"
                name="drawPolicy"
                className="field"
                defaultValue={config.drawPolicy}
                disabled={!writable}
              >
                <option value="REFUND">Refund the stake</option>
                <option value="LOSS">Treat as a loss</option>
                <option value="WIN">Treat as a win</option>
              </select>
            </div>
          </Section>

          <Section title="Hedging">
            <Num
              name="hedgeThresholdPercent"
              label="Hedge threshold (% utilisation)"
              value={config.hedgeThresholdPercent}
              disabled={!writable}
            />
            <Num
              name="criticalExposurePercent"
              label="Critical level (% utilisation)"
              value={config.criticalExposurePercent}
              disabled={!writable}
            />
            <Num
              name="minSecondsToExpiryForHedge"
              label="Minimum seconds to expiry"
              value={config.minSecondsToExpiryForHedge}
              disabled={!writable}
            />
            <Num
              name="volatilityHedgeBoost"
              label="Volatility hedge boost (0-1)"
              step="0.05"
              value={config.volatilityHedgeBoost}
              disabled={!writable}
            />
          </Section>

          <Section title="Slippage guard">
            <Num
              name="slippageTolerancePercent"
              label="Max adverse move (%)"
              step="0.05"
              value={config.slippageTolerancePercent}
              disabled={!writable}
            />
            <p className="sm:col-span-2 text-[11px] leading-relaxed text-content-400">
              Refuses a fill that moved against the trader by more than this between the price they saw and the
              server tick. The fill is always the server tick; this only decides accept or refuse. 0 turns the
              guard off.
            </p>
          </Section>

          <Section title="Early close (Sell now)">
            <Num
              name="earlyCloseFeePercent"
              label="Platform fee (%)"
              step="0.5"
              value={config.earlyCloseFeePercent}
              disabled={!writable}
            />
            <Num
              name="minSecondsRemainingForEarlyClose"
              label="Cutoff before expiry (s)"
              value={config.minSecondsRemainingForEarlyClose}
              disabled={!writable}
            />
            <OptNum
              name="earlyCloseFloorMs"
              label="Hard floor before expiry (ms, min 1000)"
              value={config.earlyCloseFloorMs}
              disabled={!writable}
            />
            <OptNum
              name="earlyCloseMaxTickAgeMs"
              label="Max price age for a close (ms)"
              value={config.earlyCloseMaxTickAgeMs}
              disabled={!writable}
            />
            <OptNum
              name="earlyCloseQuoteMaxAgeMs"
              label="Quote valid for (ms)"
              value={config.earlyCloseQuoteMaxAgeMs}
              disabled={!writable}
            />
            <OptNum
              name="earlyCloseQuoteTolerancePercent"
              label="Requote limit below the quote (%)"
              step="0.5"
              value={config.earlyCloseQuoteTolerancePercent}
              disabled={!writable}
            />
            <div className="sm:col-span-2">
              <Toggle
                name="earlyCloseEnabled"
                label="Let customers close positions early"
                defaultChecked={config.earlyCloseEnabled}
                disabled={!writable}
              />
            </div>
          </Section>

          {config.settlementMaxTickAgeMs !== undefined ? (
            <Section title="Settlement policy">
              <OptNum
                name="settlementMaxTickAgeMs"
                label="Expiry price max age (ms)"
                value={config.settlementMaxTickAgeMs}
                disabled={!writable}
              />
              <OptNum
                name="settlementConfirmationWindowMs"
                label="Confirmation wait after expiry (ms)"
                value={config.settlementConfirmationWindowMs}
                disabled={!writable}
              />
              <OptNum
                name="settlementDeferWindowMs"
                label="Keep looking for a price (ms)"
                value={config.settlementDeferWindowMs}
                disabled={!writable}
              />
              <OptNum
                name="settlementRetryDelayMs"
                label="Retry every (ms)"
                value={config.settlementRetryDelayMs}
                disabled={!writable}
              />
              <OptNum
                name="settlementMaxClockSkewMs"
                label="Ignore ticks stamped ahead by (ms)"
                value={config.settlementMaxClockSkewMs}
                disabled={!writable}
              />
              <div>
                <label className="label" htmlFor="settlementNoPriceRule">
                  No valid price
                </label>
                <select
                  id="settlementNoPriceRule"
                  name="settlementNoPriceRule"
                  className="field"
                  defaultValue={config.settlementNoPriceRule}
                  disabled={!writable}
                >
                  <option value="VOID_REFUND">Void and refund the stake</option>
                  <option value="STALE_TICK_THEN_VOID">Older pre-expiry price, else void</option>
                </select>
              </div>
              <p className="sm:col-span-2 text-[11px] leading-relaxed text-content-400">
                A price stamped after expiry is never used, whatever these say. The server refuses a clock-skew
                guard of 0, a Sell-now floor under 1 s, and a tick history shorter than the defer window plus 5
                minutes. Customer wording: docs/audit/customer/disclosures.md.
              </p>
            </Section>
          ) : null}

          <Section title="Market data thresholds">
            <Num
              name="feedHealthyMaxMs"
              label="Healthy below (ms)"
              value={config.feedHealthyMaxMs}
              disabled={!writable}
            />
            <Num
              name="feedDegradedMaxMs"
              label="Degraded below (ms)"
              value={config.feedDegradedMaxMs}
              disabled={!writable}
            />
            <Num
              name="feedTickMaxAgeMs"
              label="Offline beyond age (ms)"
              value={config.feedTickMaxAgeMs}
              disabled={!writable}
            />
            <p className="sm:col-span-2 text-[11px] leading-relaxed text-content-400">
              Healthy up to the first value, degraded up to the second, stale above it, offline beyond the
              last. These are the only feed thresholds: the FEED_* environment values only seed the first
              version.
            </p>
          </Section>

          {config.feedTickHistoryRetentionMs !== undefined ? (
            <Section title="Feed data rules">
              <OptNum
                name="feedTickHistoryRetentionMs"
                label="Tick history kept (ms)"
                value={config.feedTickHistoryRetentionMs}
                disabled={!writable}
              />
              <OptNum
                name="feedGapMinMs"
                label="Gap after silence of at least (ms)"
                value={config.feedGapMinMs}
                disabled={!writable}
              />
              <OptNum
                name="feedGapIntervalMultiple"
                label="Gap after x expected interval"
                step="0.5"
                value={config.feedGapIntervalMultiple}
                disabled={!writable}
              />
              <OptNum
                name="feedSaturationHoldMs"
                label="Stale after a dropped tick for (ms)"
                value={config.feedSaturationHoldMs}
                disabled={!writable}
              />
              <OptNum
                name="feedMaxClockDriftMs"
                label="Max clock drift vs provider (ms)"
                value={config.feedMaxClockDriftMs}
                disabled={!writable}
              />
              <OptNum
                name="feedClockCheckIntervalMs"
                label="Clock check every (ms, 0 = off)"
                value={config.feedClockCheckIntervalMs}
                disabled={!writable}
              />
            </Section>
          ) : null}

          <div className="grid gap-3 sm:grid-cols-3">
            <Toggle
              name="haltTradingOnStaleFeed"
              label="Stop new trades on a stale feed"
              defaultChecked={config.haltTradingOnStaleFeed}
              disabled={!writable}
            />
            <Toggle
              name="haltTradingOnDegradedFeed"
              label="Stop new trades on a degraded feed"
              defaultChecked={config.haltTradingOnDegradedFeed}
              disabled={!writable}
            />
            <Toggle
              name="tradingHalted"
              label="Halt all trading (kill switch)"
              defaultChecked={config.tradingHalted}
              disabled={!writable}
              danger
            />
          </div>

          {writable ? (
            <div className="flex flex-wrap items-end gap-3 border-t border-surface-800 pt-4">
              <div className="min-w-[240px] flex-1">
                <label className="label" htmlFor="note">
                  Change note
                </label>
                <input
                  id="note"
                  className="field"
                  value={note}
                  onChange={(event) => setNote(event.target.value)}
                  placeholder="Why is this changing?"
                />
              </div>
              <button type="submit" className="btn-primary" disabled={mutation.isPending}>
                Publish new version
              </button>
            </div>
          ) : null}
        </form>
      </Panel>

      <Panel className="overflow-hidden">
        <PanelHeader
          title="Hedge levels"
          description="Bands of exposure utilisation and the ratio hedged in each"
        />
        <table className="table">
          <thead>
            <tr>
              <th>Level</th>
              <th className="text-right">From</th>
              <th className="text-right">To</th>
              <th className="text-right">Hedge ratio</th>
            </tr>
          </thead>
          <tbody>
            {config.hedgeLevels.map((level) => (
              <tr key={level.name}>
                <td className="font-medium text-content-100">{level.name}</td>
                <td className="text-right tnum text-content-300">{level.fromPercent}%</td>
                <td className="text-right tnum text-content-300">
                  {level.toPercent > 1000 ? 'unbounded' : level.toPercent + '%'}
                </td>
                <td className="text-right tnum text-accent">{(level.hedgeRatio * 100).toFixed(0)}%</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Panel>

      <Panel className="overflow-hidden">
        <PanelHeader title="Version history" description="Superseded configurations are retained for audit" />
        <table className="table">
          <thead>
            <tr>
              <th>Version</th>
              <th>Published</th>
              <th className="text-right">Max gross exposure</th>
              <th className="text-right">Hedge threshold</th>
              <th>Draw policy</th>
              <th>Note</th>
            </tr>
          </thead>
          <tbody>
            {versions.map((version) => (
              <tr key={version._id} className={version.active ? '' : 'opacity-60'}>
                <td className="font-medium text-content-100">
                  v{version.version} {version.active ? <Badge tone="up">active</Badge> : null}
                </td>
                <td className="text-[11px] text-content-400">{formatDateTime(version.createdAt)}</td>
                <td className="text-right tnum text-content-300">
                  {fromMinor(version.maxGrossExposure).toLocaleString()}
                </td>
                <td className="text-right tnum text-content-300">{version.hedgeThresholdPercent}%</td>
                <td className="text-content-300">{version.drawPolicy}</td>
                <td className="text-[11px] text-content-400">{version.note ?? ''}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Panel>
    </div>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <fieldset>
      <legend className="mb-3 text-[11px] font-semibold uppercase tracking-widest text-content-400">
        {title}
      </legend>
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">{children}</div>
    </fieldset>
  );
}

/** Money inputs display major units and submit minor units. */
function Money({
  name,
  label,
  value,
  disabled,
}: {
  name: string;
  label: string;
  value: number;
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
        step="1"
        defaultValue={fromMinor(value)}
        disabled={disabled}
        className="field tnum"
      />
    </div>
  );
}

function Num({
  name,
  label,
  value,
  step,
  disabled,
}: {
  name: string;
  label: string;
  value: number;
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
        defaultValue={value}
        disabled={disabled}
        className="field tnum"
      />
    </div>
  );
}

/** A number input for a field an older server may not send. */
function OptNum(props: { name: string; label: string; value: number | undefined; step?: string; disabled?: boolean }) {
  if (props.value === undefined) return null;
  return <Num {...props} value={props.value} />;
}

function Toggle({
  name,
  label,
  defaultChecked,
  disabled,
  danger,
}: {
  name: string;
  label: string;
  defaultChecked: boolean;
  disabled?: boolean;
  danger?: boolean;
}) {
  return (
    <label
      className={
        'flex items-center gap-3 rounded-lg border px-4 py-3 text-sm ' +
        (danger
          ? 'border-critical/40 bg-critical/5 text-content-100'
          : 'border-surface-700 bg-surface-850 text-content-200')
      }
    >
      <input type="checkbox" name={name} defaultChecked={defaultChecked} disabled={disabled} />
      {label}
    </label>
  );
}
