import { useEffect, useId, useRef, useState, type ReactNode } from 'react';
import clsx from 'clsx';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Activity,
  CandlestickChart,
  Flag,
  Power,
  Rss,
  RotateCcw,
  Scale,
  Shield,
  SlidersHorizontal,
  Timer,
  TrendingDown,
  type LucideIcon,
} from 'lucide-react';
import { api } from '../services/api';
import { ApiRequestError } from '../services/http';
import { useAuthStore } from '../stores/authStore';
import { toast } from '../hooks/useToast';
import { formatDateTime, formatMs, formatRupees, fromMinor, rupeesInWords, toMinor } from '../utils/format';
import { Badge, Panel, PanelHeader, Skeleton } from '../components/ui';
import { ThemeToggle } from '../components/ThemeToggle';
import { useThemeStore } from '../stores/themeStore';

/**
 * Platform risk configuration (spec sections 13, 15, 17).
 *
 * Saving does not edit the live row: it publishes a new version and supersedes
 * the previous one, so every past risk decision can be replayed against the
 * configuration that produced it. The version history is shown underneath.
 *
 * Money crosses the wire in paise; every money field here is typed in rupees
 * and converted with toMinor on submit, exactly as before. The ₹ sign, the
 * Indian grouping and the "Off" badges are display only.
 */
export function SettingsPage() {
  const queryClient = useQueryClient();
  const can = useAuthStore((s) => s.can);
  const writable = can('risk:write');
  const [note, setNote] = useState('');
  const themePreference = useThemeStore((s) => s.preference);
  const resolvedTheme = useThemeStore((s) => s.resolved);
  const formRef = useRef<HTMLFormElement>(null);
  /** Fields that differ from the published version. */
  const [changes, setChanges] = useState(0);
  /** Bumped by Discard: remounts the fields with the published values. */
  const [formKey, setFormKey] = useState(0);

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

  // A new version (ours, or another admin's) moves the published values the
  // fields compare against, so recount once it has rendered.
  const config = query.data?.config;
  useEffect(() => {
    if (formRef.current) setChanges(countChanges(formRef.current));
  }, [config, formKey]);

  if (query.isLoading || !query.data || !config) return <Skeleton className="h-96 w-full" />;
  const { versions } = query.data;

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
            <div className="text-xs text-content-400">
              {themePreference === 'system'
                ? 'Following your device, currently ' + resolvedTheme
                : 'Always ' + (themePreference === 'dark' ? 'full night' : themePreference)}
            </div>
          </div>
          <ThemeToggle size="md" />
        </div>
      </Panel>

      <form
        key={formKey}
        ref={formRef}
        className="space-y-4"
        onChange={(event) => setChanges(countChanges(event.currentTarget))}
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
        <div className="panel flex flex-wrap items-center gap-3 px-4 py-3">
          <span className="grid h-9 w-9 shrink-0 place-items-center rounded-lg bg-accent-solid text-on-accent shadow-sm">
            <SlidersHorizontal className="h-4 w-4" />
          </span>
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <h2 className="text-base font-semibold text-content-100">Risk configuration</h2>
              <Badge tone="accent">version {config.version}</Badge>
            </div>
            <p className="text-xs text-content-400">
              Every change publishes a new version and is written to the audit log. Amounts are in rupees; the
              server stores them in paise.
            </p>
          </div>
          <div className="ml-auto flex items-center gap-2">
            {config.tradingHalted ? <Badge tone="critical">TRADING HALTED</Badge> : null}
            {writable ? null : <Badge>Read only</Badge>}
          </div>
        </div>

        <div className="grid gap-4 xl:grid-cols-2 xl:items-start">
          <div className="space-y-4">
            <Section
              icon={Scale}
              title="Exposure ceilings"
              description="Hard limits on open stake per asset, per user and across the platform."
            >
              <MoneyField
                name="maxAssetExposure"
                label="Max exposure per asset"
                value={config.maxAssetExposure}
                disabled={!writable}
              />
              <MoneyField
                name="maxUserExposure"
                label="Max exposure per user"
                value={config.maxUserExposure}
                disabled={!writable}
              />
              <MoneyField
                name="maxGrossExposure"
                label="Max platform gross exposure"
                value={config.maxGrossExposure}
                disabled={!writable}
              />
              <NumberField
                name="maxUserAssetConcentrationPercent"
                label="Max user share of one book"
                unit="%"
                hint="Of one asset's open book"
                value={config.maxUserAssetConcentrationPercent}
                disabled={!writable}
              />
              {config.maxExpiryLiability !== undefined ? (
                <>
                  <MoneyField
                    name="maxExpiryLiability"
                    label="Max liability per expiry"
                    offAtZero
                    value={config.maxExpiryLiability}
                    disabled={!writable}
                  />
                  <MoneyField
                    name="maxAssetLiability"
                    label="Max liability per asset"
                    offAtZero
                    value={config.maxAssetLiability ?? 0}
                    disabled={!writable}
                  />
                  <MoneyField
                    name="maxPlatformLiability"
                    label="Max platform liability"
                    offAtZero
                    value={config.maxPlatformLiability ?? 0}
                    disabled={!writable}
                  />
                </>
              ) : null}
            </Section>

            <Section
              icon={CandlestickChart}
              title="Trade limits"
              description="Bounds on every trade a customer places, and what a draw pays."
            >
              <MoneyField
                name="minTradeAmount"
                label="Minimum stake"
                value={config.minTradeAmount}
                disabled={!writable}
              />
              <MoneyField
                name="maxTradeAmount"
                label="Maximum stake"
                value={config.maxTradeAmount}
                disabled={!writable}
              />
              <NumberField
                name="maxOpenTradesPerUser"
                label="Max open trades per user"
                unit="trades"
                value={config.maxOpenTradesPerUser}
                disabled={!writable}
              />
              <SelectField
                name="drawPolicy"
                label="Draw policy"
                value={config.drawPolicy}
                disabled={!writable}
                options={[
                  ['REFUND', 'Refund the stake'],
                  ['LOSS', 'Treat as a loss'],
                  ['WIN', 'Treat as a win'],
                ]}
              />
            </Section>

            <Section
              icon={Shield}
              title="Hedging"
              description="When the hedging engine starts covering the book, and how hard."
            >
              <NumberField
                name="hedgeThresholdPercent"
                label="Hedge threshold"
                unit="%"
                hint="Exposure utilisation where hedging starts"
                value={config.hedgeThresholdPercent}
                disabled={!writable}
              />
              <NumberField
                name="criticalExposurePercent"
                label="Critical level"
                unit="%"
                hint="Utilisation flagged as critical"
                value={config.criticalExposurePercent}
                disabled={!writable}
              />
              <NumberField
                name="minSecondsToExpiryForHedge"
                label="Minimum time to expiry"
                unit="s"
                hint="No new hedge closer to expiry than this"
                value={config.minSecondsToExpiryForHedge}
                disabled={!writable}
              />
              <NumberField
                name="volatilityHedgeBoost"
                label="Volatility hedge boost"
                step="0.05"
                hint="Extra hedge ratio, 0 to 1"
                value={config.volatilityHedgeBoost}
                disabled={!writable}
              />
            </Section>

            <Section
              icon={TrendingDown}
              title="Slippage guard"
              description="Refuses a fill that moved too far against the trader before it reached the server."
            >
              <NumberField
                name="slippageTolerancePercent"
                label="Max adverse move"
                unit="%"
                step="0.05"
                offAtZero
                value={config.slippageTolerancePercent}
                disabled={!writable}
              />
              <Note>
                Refuses a fill that moved against the trader by more than this between the price they saw and the
                server tick. The fill is always the server tick; this only decides accept or refuse. 0 turns the
                guard off.
              </Note>
            </Section>

            <Section
              icon={Power}
              title="Trading switches"
              description="Automatic stops on a bad feed, and the kill switch for all trading."
              toggles
            >
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
            </Section>
          </div>

          <div className="space-y-4">
            <Section
              icon={Timer}
              title="Early close (Sell now)"
              description="Whether customers can sell a position before expiry, and on what terms."
            >
              <NumberField
                name="earlyCloseFeePercent"
                label="Platform fee"
                unit="%"
                step="0.5"
                value={config.earlyCloseFeePercent}
                disabled={!writable}
              />
              <NumberField
                name="minSecondsRemainingForEarlyClose"
                label="Cutoff before expiry"
                unit="s"
                value={config.minSecondsRemainingForEarlyClose}
                disabled={!writable}
              />
              <OptNumberField
                name="earlyCloseFloorMs"
                label="Hard floor before expiry"
                unit="ms"
                hint="At least 1,000 ms"
                value={config.earlyCloseFloorMs}
                disabled={!writable}
              />
              <OptNumberField
                name="earlyCloseMaxTickAgeMs"
                label="Max price age for a close"
                unit="ms"
                value={config.earlyCloseMaxTickAgeMs}
                disabled={!writable}
              />
              <OptNumberField
                name="earlyCloseQuoteMaxAgeMs"
                label="Quote valid for"
                unit="ms"
                value={config.earlyCloseQuoteMaxAgeMs}
                disabled={!writable}
              />
              <OptNumberField
                name="earlyCloseQuoteTolerancePercent"
                label="Requote limit below the quote"
                unit="%"
                step="0.5"
                value={config.earlyCloseQuoteTolerancePercent}
                disabled={!writable}
              />
              <div className="col-span-full">
                <Toggle
                  name="earlyCloseEnabled"
                  label="Let customers close positions early"
                  defaultChecked={config.earlyCloseEnabled}
                  disabled={!writable}
                />
              </div>
            </Section>

            {config.settlementMaxTickAgeMs !== undefined ? (
              <Section
                icon={Flag}
                title="Settlement policy"
                description="Which price settles a trade at expiry, and what happens without one."
              >
                <OptNumberField
                  name="settlementMaxTickAgeMs"
                  label="Expiry price max age"
                  unit="ms"
                  value={config.settlementMaxTickAgeMs}
                  disabled={!writable}
                />
                <OptNumberField
                  name="settlementConfirmationWindowMs"
                  label="Confirmation wait after expiry"
                  unit="ms"
                  value={config.settlementConfirmationWindowMs}
                  disabled={!writable}
                />
                <OptNumberField
                  name="settlementDeferWindowMs"
                  label="Keep looking for a price"
                  unit="ms"
                  value={config.settlementDeferWindowMs}
                  disabled={!writable}
                />
                <OptNumberField
                  name="settlementRetryDelayMs"
                  label="Retry every"
                  unit="ms"
                  value={config.settlementRetryDelayMs}
                  disabled={!writable}
                />
                <OptNumberField
                  name="settlementMaxClockSkewMs"
                  label="Ignore ticks stamped ahead by"
                  unit="ms"
                  value={config.settlementMaxClockSkewMs}
                  disabled={!writable}
                />
                <SelectField
                  name="settlementNoPriceRule"
                  label="No valid price"
                  value={config.settlementNoPriceRule}
                  disabled={!writable}
                  options={[
                    ['VOID_REFUND', 'Void and refund the stake'],
                    ['STALE_TICK_THEN_VOID', 'Older pre-expiry price, else void'],
                  ]}
                />
                <Note>
                  A price stamped after expiry is never used, whatever these say. The server refuses a clock-skew
                  guard of 0, a Sell-now floor under 1 s, and a tick history shorter than the defer window plus 5
                  minutes. Customer wording: docs/audit/customer/disclosures.md.
                </Note>
              </Section>
            ) : null}

            <Section
              icon={Activity}
              title="Market data thresholds"
              description="Feed latency bands that mark an asset healthy, degraded, stale or offline."
            >
              <NumberField
                name="feedHealthyMaxMs"
                label="Healthy below"
                unit="ms"
                value={config.feedHealthyMaxMs}
                disabled={!writable}
              />
              <NumberField
                name="feedDegradedMaxMs"
                label="Degraded below"
                unit="ms"
                value={config.feedDegradedMaxMs}
                disabled={!writable}
              />
              <NumberField
                name="feedTickMaxAgeMs"
                label="Offline beyond age"
                unit="ms"
                value={config.feedTickMaxAgeMs}
                disabled={!writable}
              />
              <Note>
                Healthy up to the first value, degraded up to the second, stale above it, offline beyond the
                last. These are the only feed thresholds: the FEED_* environment values only seed the first
                version.
              </Note>
            </Section>

            {config.feedTickHistoryRetentionMs !== undefined ? (
              <Section
                icon={Rss}
                title="Feed data rules"
                description="How long ticks are kept, and when a gap, a dropped tick or clock drift counts."
              >
                <OptNumberField
                  name="feedTickHistoryRetentionMs"
                  label="Tick history kept"
                  unit="ms"
                  value={config.feedTickHistoryRetentionMs}
                  disabled={!writable}
                />
                <OptNumberField
                  name="feedGapMinMs"
                  label="Gap after silence of at least"
                  unit="ms"
                  value={config.feedGapMinMs}
                  disabled={!writable}
                />
                <OptNumberField
                  name="feedGapIntervalMultiple"
                  label="Gap after the expected interval"
                  unit="×"
                  step="0.5"
                  hint="Times the asset's expected interval"
                  value={config.feedGapIntervalMultiple}
                  disabled={!writable}
                />
                <OptNumberField
                  name="feedSaturationHoldMs"
                  label="Stale after a dropped tick for"
                  unit="ms"
                  offAtZero
                  offText="Off - dropped ticks are only counted"
                  value={config.feedSaturationHoldMs}
                  disabled={!writable}
                />
                <OptNumberField
                  name="feedMaxClockDriftMs"
                  label="Max clock drift vs provider"
                  unit="ms"
                  value={config.feedMaxClockDriftMs}
                  disabled={!writable}
                />
                <OptNumberField
                  name="feedClockCheckIntervalMs"
                  label="Clock check every"
                  unit="ms"
                  offAtZero
                  value={config.feedClockCheckIntervalMs}
                  disabled={!writable}
                />
              </Section>
            ) : null}
          </div>
        </div>

        {writable ? (
          <div className="sticky bottom-3 z-10">
            <div className="panel flex flex-wrap items-end gap-3 px-4 py-3 shadow-overlay">
              <div className="flex items-center gap-2 self-center" aria-live="polite">
                {changes > 0 ? (
                  <>
                    <span className="grid h-6 min-w-6 place-items-center rounded-full bg-accent-solid px-1.5 text-xs font-bold text-on-accent tnum">
                      {changes}
                    </span>
                    <span className="text-sm font-medium text-content-100">
                      {changes === 1 ? 'unsaved change' : 'unsaved changes'}
                    </span>
                  </>
                ) : (
                  <span className="text-sm text-content-400">No unsaved changes</span>
                )}
              </div>
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
              {changes > 0 ? (
                <button
                  type="button"
                  className="btn-ghost"
                  onClick={() => {
                    setFormKey((key) => key + 1);
                    setChanges(0);
                  }}
                >
                  <RotateCcw className="h-4 w-4" />
                  Discard
                </button>
              ) : null}
              <button type="submit" className="btn-primary" disabled={mutation.isPending}>
                Publish new version
              </button>
            </div>
          </div>
        ) : null}
      </form>

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
                  {formatRupees(fromMinor(version.maxGrossExposure))}
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

/**
 * How many fields differ from the published version. Each field carries the
 * published value as data-published (React keeps a data attribute current,
 * where it never moves a select's defaultSelected), so this needs no second
 * list of the fields. The change note has none and is not counted.
 */
function countChanges(form: HTMLFormElement): number {
  let count = 0;
  for (const element of Array.from(form.elements)) {
    if (!(element instanceof HTMLInputElement || element instanceof HTMLSelectElement)) continue;
    const published = element.dataset.published;
    if (published === undefined) continue;
    const current =
      element instanceof HTMLInputElement && element.type === 'checkbox'
        ? element.checked
          ? 'on'
          : 'off'
        : element.value;
    if (current !== published) count += 1;
  }
  return count;
}

/** One settings group: a card with an icon, a title and a one-line description. */
function Section({
  icon: Icon,
  title,
  description,
  toggles,
  children,
}: {
  icon: LucideIcon;
  title: string;
  description: string;
  /** A list of switches, one to a row in the two-column layout. */
  toggles?: boolean;
  children: ReactNode;
}) {
  const id = useId();
  return (
    <fieldset aria-labelledby={id} className="panel min-w-0">
      <div className="flex items-start gap-3 border-b border-surface-800 px-4 py-3">
        <span className="grid h-8 w-8 shrink-0 place-items-center rounded-lg border border-surface-700 bg-surface-850 text-accent">
          <Icon className="h-4 w-4" />
        </span>
        <div className="min-w-0">
          <h3 id={id} className="text-sm font-semibold text-content-100">
            {title}
          </h3>
          <p className="mt-0.5 text-xs text-content-400">{description}</p>
        </div>
      </div>
      <div
        className={clsx(
          'grid p-4',
          toggles ? 'gap-3 sm:grid-cols-3 xl:grid-cols-1' : 'gap-x-3 gap-y-4 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-2',
        )}
      >
        {children}
      </div>
    </fieldset>
  );
}

/** A full-width explanatory paragraph inside a section. */
function Note({ children }: { children: ReactNode }) {
  return <p className="col-span-full text-xs leading-relaxed text-content-400">{children}</p>;
}

/** The label row shared by every field, with an "Off" badge for a 0 = off setting at 0. */
function FieldLabel({ htmlFor, label, off }: { htmlFor: string; label: string; off?: boolean }) {
  return (
    <div className="mb-1.5 flex min-h-[20px] items-center gap-2">
      <label className="text-xs font-medium text-content-200" htmlFor={htmlFor}>
        {label}
      </label>
      {off ? (
        <Badge className="ml-auto" tone="neutral">
          Off
        </Badge>
      ) : null}
    </div>
  );
}

/** The line under an input: a live reading of the value, a hint, or nothing. */
function FieldHelp({ children }: { children: ReactNode }) {
  return <p className="mt-1 min-h-[16px] text-[11px] leading-4 text-content-400">{children}</p>;
}

/**
 * Tracks what is typed in an uncontrolled input, for the help line. Re-reads
 * the input when the published value changes, because React then moves an
 * untouched field to the new value without an input event.
 */
function useTyped(initial: string) {
  const ref = useRef<HTMLInputElement>(null);
  const [typed, setTyped] = useState(initial);
  useEffect(() => {
    setTyped(ref.current ? ref.current.value : initial);
  }, [initial]);
  return { ref, typed, setTyped };
}

/**
 * Money: typed in rupees, submitted in paise (toMinor in the submit handler).
 * Shows the ₹ sign in the field and the amount with Indian grouping and in
 * words under it.
 */
function MoneyField({
  name,
  label,
  value,
  offAtZero,
  disabled,
}: {
  name: string;
  label: string;
  /** Paise, as the API sends it. */
  value: number;
  /** 0 disables the rule this field sets. */
  offAtZero?: boolean;
  disabled?: boolean;
}) {
  const initial = String(fromMinor(value));
  const { ref, typed, setTyped } = useTyped(initial);
  const rupees = typed.trim() === '' ? NaN : Number(typed);
  const off = offAtZero && rupees === 0;
  const words = Number.isFinite(rupees) ? rupeesInWords(rupees) : '';

  return (
    <div className="flex min-w-0 flex-col">
      <FieldLabel htmlFor={name} label={label} off={off} />
      <div className="relative mt-auto">
        <span className="pointer-events-none absolute inset-y-0 left-3 flex items-center text-sm font-semibold text-content-300">
          ₹
        </span>
        <input
          ref={ref}
          id={name}
          name={name}
          type="number"
          step="1"
          inputMode="decimal"
          defaultValue={initial}
          data-published={initial}
          disabled={disabled}
          onChange={(event) => setTyped(event.target.value)}
          className="field no-spin pl-7 tnum"
        />
      </div>
      <FieldHelp>
        {!Number.isFinite(rupees) ? (
          'Enter an amount in rupees'
        ) : off ? (
          'Off - no limit'
        ) : (
          <>
            <span className="font-medium text-content-200 tnum">{formatRupees(rupees)}</span>
            {words ? ' · ' + words : null}
            {offAtZero ? ' · 0 turns it off' : null}
          </>
        )}
      </FieldHelp>
    </div>
  );
}

/** A number with its unit drawn inside the field; ms values are also read out in s / min / h. */
function NumberField({
  name,
  label,
  value,
  unit,
  step,
  hint,
  offAtZero,
  offText = 'Off',
  disabled,
}: {
  name: string;
  label: string;
  value: number;
  unit?: string;
  step?: string;
  hint?: string;
  /** 0 disables the rule this field sets. */
  offAtZero?: boolean;
  offText?: string;
  disabled?: boolean;
}) {
  const initial = String(value);
  const { ref, typed, setTyped } = useTyped(initial);
  const current = typed.trim() === '' ? NaN : Number(typed);
  const off = offAtZero && current === 0;
  const readable = unit === 'ms' && Number.isFinite(current) ? formatMs(current) : '';
  const help = off
    ? offText
    : [readable ? '= ' + readable : '', hint ?? '', offAtZero ? '0 turns it off' : ''].filter(Boolean).join(' · ');

  return (
    <div className="flex min-w-0 flex-col">
      <FieldLabel htmlFor={name} label={label} off={off} />
      <div className="relative mt-auto">
        <input
          ref={ref}
          id={name}
          name={name}
          type="number"
          step={step ?? '1'}
          defaultValue={initial}
          data-published={initial}
          disabled={disabled}
          onChange={(event) => setTyped(event.target.value)}
          className={clsx('field no-spin tnum', unit ? (unit.length > 2 ? 'pr-16' : 'pr-10') : null)}
        />
        {unit ? (
          <span className="pointer-events-none absolute inset-y-0 right-3 flex items-center text-xs font-semibold text-content-400">
            {unit}
          </span>
        ) : null}
      </div>
      <FieldHelp>{help}</FieldHelp>
    </div>
  );
}

/** A number field for a setting an older server may not send. */
function OptNumberField(props: Omit<Parameters<typeof NumberField>[0], 'value'> & { value: number | undefined }) {
  if (props.value === undefined) return null;
  return <NumberField {...props} value={props.value} />;
}

function SelectField({
  name,
  label,
  value,
  options,
  disabled,
}: {
  name: string;
  label: string;
  /** Undefined only from an older server: the browser then shows the first option. */
  value: string | undefined;
  options: Array<[string, string]>;
  disabled?: boolean;
}) {
  return (
    <div className="flex min-w-0 flex-col">
      <FieldLabel htmlFor={name} label={label} />
      <select
        id={name}
        name={name}
        className="field mt-auto"
        defaultValue={value}
        data-published={value ?? options[0]?.[0]}
        disabled={disabled}
      >
        {options.map(([optionValue, text]) => (
          <option key={optionValue} value={optionValue}>
            {text}
          </option>
        ))}
      </select>
      <FieldHelp>{null}</FieldHelp>
    </div>
  );
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
      className={clsx(
        'flex cursor-pointer items-center gap-3 rounded-lg border px-4 py-3 text-sm font-medium transition',
        danger
          ? 'border-critical/50 bg-critical/10 text-content-100 hover:border-critical/70'
          : 'border-surface-700 bg-surface-850 text-content-200 hover:border-surface-600 hover:text-content-100',
        disabled ? 'cursor-not-allowed opacity-70' : null,
      )}
    >
      <input
        type="checkbox"
        className="h-4 w-4 shrink-0"
        name={name}
        defaultChecked={defaultChecked}
        data-published={defaultChecked ? 'on' : 'off'}
        disabled={disabled}
      />
      {label}
    </label>
  );
}
