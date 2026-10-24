import { useEffect, useRef, useState, type ReactNode } from 'react';
import { Link } from 'react-router-dom';
import clsx from 'clsx';
import { useQuery } from '@tanstack/react-query';
import {
  ArrowUpRight,
  ChevronDown,
  Coins,
  Cpu,
  Filter,
  Gauge,
  Hourglass,
  Info,
  Plus,
  Receipt,
  Scale,
  ShieldCheck,
  Timer,
  Trash2,
  Umbrella,
  Waves,
  type LucideIcon,
} from 'lucide-react';
import { api } from '../services/api';
import { useAuthStore } from '../stores/authStore';
import { formatExpiry, formatMs, formatRupees, fromMinor, rupeesInWords } from '../utils/format';
import {
  EXPIRY_SECONDS_MAX,
  FACTOR_MAX,
  FACTOR_MIN,
  LAUNCH_SYMBOLS,
  PRICING_GROUPS,
  PRICING_SPEC_CHOICES,
  PRICING_UNITS,
  RULE_FIELDS,
  SPEC_EXPIRY_FACTORS,
  SPEC_SYMBOL_RULES,
  annualisedVolPercent,
  checkValue,
  expiryInput,
  ruleInput,
  ruleTemplate,
  shownSymbols,
  toApi,
  toDisplay,
  withUnit,
  type PricingFieldSpec,
  type PricingUnit,
  type RuleFieldSpec,
  type RuleNumberKey,
} from '../utils/pricingSettings';
import { Badge } from '../components/ui';
import { FieldLabel, Toggle, useTyped } from '../components/settingsFields';
import type { AdminAsset, PricingExpiryFactor, PricingSymbolRule, RiskConfig } from '../types/api';

/**
 * The pricing engine's parameters on the Settings page
 * (docs/audit/pricing/pricing-spec.md section 11).
 *
 * Part of the Settings form: the fields are uncontrolled and carry their
 * published value as data-published, so they count in the sticky save bar and
 * go out in the same versioned PUT /admin/settings/risk, with the same change
 * note and audit entry, as the rest of the page. Values are shown in operator
 * units (points, percent, rupees) and converted back exactly on submit
 * (utils/pricingSettings.ts).
 *
 * Kept elsewhere on purpose: live payouts and the halt switches are on the
 * Payouts page (PricingConsole), and each expiry's base payout with its floor,
 * ceiling and margin overrides is on the asset page.
 */
export function PricingEngineSettings({
  config,
  writable,
  sectionChanges,
  onRowsChange,
}: {
  config: RiskConfig;
  writable: boolean;
  /** Unsaved changes per group (data-section), from the form. */
  sectionChanges: Record<string, number>;
  /** A table gained or lost a row: no change event fires, so the form recounts. */
  onRowsChange: () => void;
}) {
  const disabled = !writable;
  const icons: Record<string, LucideIcon> = {
    'market-data': Filter,
    volatility: Waves,
    'spread-exposure': Scale,
    offers: Receipt,
    guard: ShieldCheck,
    hedge: Umbrella,
    'sell-now': Hourglass,
  };
  const groups = Object.fromEntries(PRICING_GROUPS.map((group) => [group.id, group]));
  const lambdaReadout = (lambda: number) => lambdaMemory(lambda, config.pricingVolSampleIntervalMs);
  const numberFields = (id: string) =>
    groups[id].fields.map((spec) => (
      <PricingNumberField
        key={spec.key}
        spec={spec}
        value={config[spec.key]}
        disabled={disabled}
        readout={spec.key === 'pricingEwmaLambda' ? lambdaReadout : undefined}
      />
    ));
  const group = (id: string, children: ReactNode, open = false) => (
    <PricingGroup
      id={id}
      icon={icons[id]}
      title={groups[id].title}
      description={groups[id].description}
      changed={sectionChanges[id]}
      defaultOpen={open}
    >
      {children}
    </PricingGroup>
  );

  return (
    <section className="space-y-4" aria-label="Pricing engine">
      <div className="panel">
        <div className="flex flex-wrap items-center gap-3 px-4 py-3">
          <span className="grid h-9 w-9 shrink-0 place-items-center rounded-lg bg-accent-solid text-on-accent shadow-sm">
            <Gauge className="h-4 w-4" />
          </span>
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <h2 className="text-base font-semibold text-content-100">Pricing engine</h2>
              <Badge tone={config.pricingMode === 'FIXED' ? 'warn' : 'accent'}>{config.pricingMode}</Badge>
              <Badge>
                {config.pricingRuleLabel}+rc{config.version}
              </Badge>
            </div>
            <p className="text-xs text-content-400">
              Every dynamic-pricing parameter, in points, percent and rupees. Published with the rest of this page.
            </p>
          </div>
          <Link to="/admin/payouts" className="btn-ghost ml-auto px-3 py-1.5 text-xs">
            Live payouts and halts
            <ArrowUpRight className="h-3.5 w-3.5" />
          </Link>
        </div>
        <div className="border-t border-surface-800 px-4 py-3">
          <div className="flex gap-3 rounded-lg border border-accent/30 bg-accent/10 px-3 py-2.5">
            <Info className="mt-0.5 h-4 w-4 shrink-0 text-accent" />
            <div className="min-w-0 text-xs leading-relaxed text-content-200">
              <div className="font-semibold text-content-100">What happens when you change this</div>
              <ul className="mt-1 list-disc space-y-0.5 pl-4">
                <li>
                  Payouts change for <strong>new offers only</strong>: every quote issued after you publish uses the
                  new values (an offer lives {formatMs(config.pricingOfferTtlMs ?? 3000) || '3 s'}).
                </li>
                <li>Open trades keep the terms they were accepted on, and settle on them.</li>
                <li>
                  Every change is a new version: its rule version ({config.pricingRuleLabel}+rc&lt;version&gt;+p&lt;payout
                  row&gt;) is recorded on each offer and trade, and the change, with your note, in the audit log.
                </li>
              </ul>
              <p className="mt-1.5 text-content-400">
                Each expiry&apos;s base payout and its floor, ceiling and margin overrides are set on the asset&apos;s
                page; halting a symbol is on the Payouts page.
              </p>
            </div>
          </div>
        </div>
      </div>

      <div className="grid gap-4 xl:grid-cols-2 xl:items-start">
        <div className="space-y-4">
          <PricingGroup
            id="mode"
            icon={Cpu}
            title="Mode and rule set"
            description="Whether the formula prices trades, and the label stamped on every offer."
            changed={sectionChanges.mode}
            defaultOpen
          >
            <PricingSelect
              name="pricingMode"
              label="Pricing mode"
              help="Dynamic prices every trade with the formula; fixed pays each payout row's percent."
              value={config.pricingMode ?? PRICING_SPEC_CHOICES.pricingMode}
              spec={PRICING_SPEC_CHOICES.pricingMode}
              options={[
                ['DYNAMIC', 'Dynamic (the formula)'],
                ['FIXED', 'Fixed (payout row percent)'],
              ]}
              disabled={disabled}
            />
            <PricingTextField
              name="pricingRuleLabel"
              label="Rule set label"
              help="Stamped on each offer and trade as label+rc<version>+p<row>."
              value={config.pricingRuleLabel ?? PRICING_SPEC_CHOICES.pricingRuleLabel}
              spec={PRICING_SPEC_CHOICES.pricingRuleLabel}
              disabled={disabled}
            />
          </PricingGroup>
          {group('volatility', numberFields('volatility'), true)}
          {group('spread-exposure', numberFields('spread-exposure'), true)}
          {group('market-data', numberFields('market-data'))}
        </div>

        <div className="space-y-4">
          <PricingGroup
            id="expiry"
            icon={Timer}
            title="Expiry factors"
            description="Scale the volatility and hedge deductions by expiry; an expiry not listed uses 1×."
            changed={sectionChanges.expiry}
            defaultOpen
            plain
          >
            {/* Keyed on the version: a publish (ours or another admin's) starts from the new table. */}
            <ExpiryFactors
              key={config.version}
              published={config.pricingExpiryFactors ?? []}
              disabled={disabled}
              onRowsChange={onRowsChange}
            />
          </PricingGroup>
          {group('offers', numberFields('offers'))}
          {group(
            'guard',
            <>
              <div className="col-span-full">
                <Toggle
                  name="pricingGuardEnabled"
                  label="Use the calibrated safety guard"
                  hint="Refuses an offer the win-probability model says loses money; never raises a payout. Default on."
                  defaultChecked={config.pricingGuardEnabled ?? PRICING_SPEC_CHOICES.pricingGuardEnabled}
                  disabled={disabled}
                />
              </div>
              {numberFields('guard')}
              <div className="col-span-full">
                <Toggle
                  name="pricingGuardConservative"
                  label="Check the conservative bound"
                  hint="Probabilities plus their calibrated uncertainty, not the point estimate; refuses far more. Default off."
                  defaultChecked={config.pricingGuardConservative ?? PRICING_SPEC_CHOICES.pricingGuardConservative}
                  disabled={disabled}
                />
              </div>
            </>,
          )}
          {group(
            'hedge',
            <>
              <PricingSelect
                name="pricingHedgeUnavailableAction"
                label="When a needed hedge is unavailable"
                help="Widen takes the outage deduction off; refuse stops offers on that asset."
                value={config.pricingHedgeUnavailableAction ?? PRICING_SPEC_CHOICES.pricingHedgeUnavailableAction}
                spec={PRICING_SPEC_CHOICES.pricingHedgeUnavailableAction}
                options={[
                  ['WIDEN', 'Widen (lower payouts)'],
                  ['REFUSE', 'Refuse (no offers)'],
                ]}
                disabled={disabled}
              />
              {numberFields('hedge')}
            </>,
          )}
          {group('sell-now', numberFields('sell-now'))}
        </div>
      </div>

      <PricingGroup
        id="symbols"
        icon={Coins}
        title="Instrument rules"
        description="Each instrument's payouts, normal volatility, costs, exposure limit and stake range."
        changed={sectionChanges.symbols}
        defaultOpen
        plain
      >
        <SymbolRules
          key={config.version}
          rules={config.pricingSymbolRules ?? []}
          disabled={disabled}
          onRowsChange={onRowsChange}
        />
      </PricingGroup>
    </section>
  );
}

/** Memory of the volatility estimate: about 1 / (1 - λ) observations. */
function lambdaMemory(lambda: number, intervalMs: number | undefined): string {
  if (!(lambda > 0 && lambda < 1)) return '';
  const observations = 1 / (1 - lambda);
  if (!intervalMs) return '≈ ' + Math.round(observations) + ' quotes of memory';
  return '≈ ' + (formatMs(observations * intervalMs) || Math.round(observations * intervalMs) + ' ms') + ' of memory';
}

/** A collapsible group of the pricing card; says how many unsaved changes it holds while closed. */
function PricingGroup({
  id,
  icon: Icon,
  title,
  description,
  changed,
  defaultOpen,
  plain,
  children,
}: {
  id: string;
  icon: LucideIcon;
  title: string;
  description: string;
  changed?: number;
  defaultOpen?: boolean;
  /** The children lay themselves out (tables) instead of the field grid. */
  plain?: boolean;
  children: ReactNode;
}) {
  return (
    <details data-section={id} open={defaultOpen} className="panel group min-w-0">
      <summary className="flex cursor-pointer list-none items-start gap-3 px-4 py-3 [&::-webkit-details-marker]:hidden">
        <span className="grid h-8 w-8 shrink-0 place-items-center rounded-lg border border-surface-700 bg-surface-850 text-accent">
          <Icon className="h-4 w-4" />
        </span>
        <span className="min-w-0 flex-1">
          <span className="block text-sm font-semibold text-content-100">{title}</span>
          <span className="mt-0.5 block text-xs text-content-400">{description}</span>
        </span>
        {changed ? <Badge tone="accent">{changed === 1 ? '1 change' : changed + ' changes'}</Badge> : null}
        <ChevronDown className="mt-1 h-4 w-4 shrink-0 text-content-400 transition group-open:rotate-180" />
      </summary>
      <div
        className={clsx(
          'border-t border-surface-800',
          plain ? null : 'grid gap-x-3 gap-y-4 p-4 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-2',
        )}
      >
        {children}
      </div>
    </details>
  );
}

/** The two quiet lines under a pricing field: what it does, then the default (or what is wrong). */
function PricingHelp({
  id,
  help,
  problem,
  children,
}: {
  id: string;
  help: string;
  problem: string | null;
  children: ReactNode;
}) {
  return (
    <div id={id} className="mt-1 text-[11px] leading-4">
      <p className="text-content-400">{help}</p>
      <p className={clsx('mt-0.5 min-h-[16px]', problem ? 'font-medium text-down' : 'text-content-400')}>
        {problem ?? children}
      </p>
    </div>
  );
}

function UnitSuffix({ unit }: { unit: string }) {
  return unit ? (
    <span className="pointer-events-none absolute inset-y-0 right-3 flex items-center text-xs font-semibold text-content-400">
      {unit}
    </span>
  ) : null;
}

function unitPadding(unit: string): string | null {
  if (!unit) return null;
  return unit.length > 3 ? 'pr-16' : 'pr-11';
}

/** A numeric parameter shown in its operator unit; renders nothing when the server does not send it. */
function PricingNumberField(props: {
  spec: PricingFieldSpec;
  value: number | undefined;
  disabled: boolean;
  readout?: (shown: number) => string;
}) {
  if (props.value === undefined) return null;
  return <PricingNumberInput {...props} value={props.value} />;
}

function PricingNumberInput({
  spec,
  value,
  disabled,
  readout,
}: {
  spec: PricingFieldSpec;
  value: number;
  disabled: boolean;
  readout?: (shown: number) => string;
}) {
  const initial = String(toDisplay(value, spec.unit));
  const { ref, typed, setTyped } = useTyped(initial);
  const problem = checkValue(spec, typed);
  const current = Number(typed);
  const suffix = PRICING_UNITS[spec.unit].suffix;
  const extra = [
    'Default ' + withUnit(toDisplay(spec.spec, spec.unit), spec.unit),
    typed.trim() !== initial ? 'was ' + withUnit(Number(initial), spec.unit) : '',
    spec.unit === 'ms' && current >= 1000 ? '= ' + formatMs(current) : '',
    spec.zero && current === 0 ? '0 = ' + spec.zero : '',
    readout && Number.isFinite(current) ? readout(current) : '',
  ].filter(Boolean);

  return (
    <div className="flex min-w-0 flex-col">
      <FieldLabel htmlFor={spec.key} label={spec.label} />
      <div className="relative">
        <input
          ref={ref}
          id={spec.key}
          name={spec.key}
          type="number"
          step="any"
          inputMode="decimal"
          defaultValue={initial}
          data-published={initial}
          disabled={disabled}
          aria-invalid={problem ? true : undefined}
          aria-describedby={spec.key + '-help'}
          onChange={(event) => setTyped(event.target.value)}
          className={clsx('field no-spin tnum', unitPadding(suffix), problem ? 'border-down/70' : null)}
        />
        <UnitSuffix unit={suffix} />
      </div>
      <PricingHelp id={spec.key + '-help'} help={spec.help} problem={problem}>
        {extra.join(' · ')}
      </PricingHelp>
    </div>
  );
}

function PricingSelect({
  name,
  label,
  help,
  value,
  spec,
  options,
  disabled,
}: {
  name: string;
  label: string;
  help: string;
  value: string;
  spec: string;
  options: Array<[string, string]>;
  disabled: boolean;
}) {
  const specText = options.find(([option]) => option === spec)?.[1] ?? spec;
  return (
    <div className="flex min-w-0 flex-col">
      <FieldLabel htmlFor={name} label={label} />
      <select
        id={name}
        name={name}
        className="field"
        defaultValue={value}
        data-published={value}
        disabled={disabled}
        aria-describedby={name + '-help'}
      >
        {options.map(([option, text]) => (
          <option key={option} value={option}>
            {text}
          </option>
        ))}
      </select>
      <PricingHelp id={name + '-help'} help={help} problem={null}>
        Default {specText.replace(/ \(.*\)$/, '').toLowerCase()}
      </PricingHelp>
    </div>
  );
}

function PricingTextField({
  name,
  label,
  help,
  value,
  spec,
  disabled,
}: {
  name: string;
  label: string;
  help: string;
  value: string;
  spec: string;
  disabled: boolean;
}) {
  const { ref, typed, setTyped } = useTyped(value);
  const length = typed.trim().length;
  const problem = length < 3 || length > 60 ? '3 to 60 characters' : null;
  return (
    <div className="flex min-w-0 flex-col">
      <FieldLabel htmlFor={name} label={label} />
      <input
        ref={ref}
        id={name}
        name={name}
        className={clsx('field', problem ? 'border-down/70' : null)}
        defaultValue={value}
        data-published={value}
        disabled={disabled}
        maxLength={60}
        aria-describedby={name + '-help'}
        onChange={(event) => setTyped(event.target.value)}
      />
      <PricingHelp id={name + '-help'} help={help} problem={problem}>
        Default {spec}
      </PricingHelp>
    </div>
  );
}

/** Top padding that centres a line of small text on a compact field in the next cell. */
const TEXT_BESIDE_INPUT = 'pt-[17px]';

// ── expiry factors ──────────────────────────────────────────────────────

interface FactorRow {
  id: string;
  /** The published row, or null for a row added here. */
  published: PricingExpiryFactor | null;
}

/**
 * The expiry-factor table. Published rows keep their expiry (remove and add a
 * row to move one); rows added here take one. A removed published row leaves
 * a hidden marker so it counts as a change; an added row counts as one.
 */
function ExpiryFactors({
  published,
  disabled,
  onRowsChange,
}: {
  published: PricingExpiryFactor[];
  disabled: boolean;
  onRowsChange: () => void;
}) {
  const sortedPublished = [...published].sort((a, b) => a.expirySeconds - b.expirySeconds);
  const [rows, setRows] = useState<FactorRow[]>(() =>
    sortedPublished.map((factor) => ({ id: 'p' + factor.expirySeconds, published: factor })),
  );
  const nextId = useRef(0);
  const firstRender = useRef(true);
  useEffect(() => {
    if (firstRender.current) {
      firstRender.current = false;
      return;
    }
    onRowsChange();
  }, [rows, onRowsChange]);

  const kept = new Set(rows.map((row) => row.published?.expirySeconds));
  const removed = sortedPublished.filter((factor) => !kept.has(factor.expirySeconds));

  return (
    <div className="min-w-0">
      <table className="table">
        <thead>
          <tr>
            <th>Expiry</th>
            <th className="w-40">Factor</th>
            <th>Default</th>
            {disabled ? null : <th className="w-12" aria-label="Remove" />}
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <FactorRowView
              key={row.id}
              row={row}
              disabled={disabled}
              onRemove={() => setRows((current) => current.filter((r) => r.id !== row.id))}
            />
          ))}
          {rows.length === 0 ? (
            <tr>
              <td colSpan={4} className="text-xs text-content-400">
                No factors: every expiry uses 1×.
              </td>
            </tr>
          ) : null}
        </tbody>
      </table>
      {removed.map((factor) => (
        <input key={factor.expirySeconds} type="hidden" data-published="kept" value="removed" />
      ))}
      <div className="flex flex-wrap items-center gap-3 border-t border-surface-800 px-4 py-3">
        {disabled ? null : (
          <button
            type="button"
            className="btn-ghost px-3 py-1.5 text-xs"
            onClick={() => setRows((current) => [...current, { id: 'n' + nextId.current++, published: null }])}
          >
            <Plus className="h-3.5 w-3.5" />
            Add an expiry
          </button>
        )}
        <p className="text-[11px] leading-4 text-content-400">
          The factor multiplies the volatility and hedge deductions at that expiry: above 1× short expiries pay less.
          Defaults: {SPEC_EXPIRY_FACTORS.map((f) => formatExpiry(f.expirySeconds) + ' ' + f.factor + '×').join(', ')}.
        </p>
      </div>
    </div>
  );
}

function FactorRowView({ row, disabled, onRemove }: { row: FactorRow; disabled: boolean; onRemove: () => void }) {
  const initialSeconds = row.published ? String(row.published.expirySeconds) : '';
  const initialFactor = row.published ? String(row.published.factor) : '1';
  const seconds = useTyped(initialSeconds);
  const factor = useTyped(initialFactor);
  const secondsProblem = row.published
    ? null
    : checkValue({ min: 1, max: EXPIRY_SECONDS_MAX, integer: true, unit: 'plain' }, seconds.typed);
  const factorProblem = checkValue({ min: FACTOR_MIN, max: FACTOR_MAX, unit: 'times' }, factor.typed);
  const expiry = Number(seconds.typed);
  const spec = SPEC_EXPIRY_FACTORS.find((f) => f.expirySeconds === expiry);
  const secondsName = expiryInput(row.id, 'seconds');
  const factorName = expiryInput(row.id, 'factor');

  return (
    <tr>
      <td className={clsx('align-top', row.published ? TEXT_BESIDE_INPUT : null)}>
        {row.published ? (
          <>
            <input type="hidden" name={secondsName} value={initialSeconds} />
            <span className="font-medium text-content-100">{formatExpiry(row.published.expirySeconds)}</span>
            <span className="ml-2 text-[11px] text-content-400 tnum">{row.published.expirySeconds} s</span>
          </>
        ) : (
          <>
            <input type="hidden" data-published="absent" value="added" />
            <div className="relative w-32">
              <input
                ref={seconds.ref}
                name={secondsName}
                aria-label="Expiry in seconds"
                type="number"
                step="any"
                inputMode="numeric"
                defaultValue={initialSeconds}
                placeholder="e.g. 900"
                disabled={disabled}
                onChange={(event) => seconds.setTyped(event.target.value)}
                className={clsx('field no-spin py-1.5 pr-8 tnum', secondsProblem ? 'border-down/70' : null)}
              />
              <UnitSuffix unit="s" />
            </div>
            <p className={clsx('mt-1 text-[11px] leading-4', secondsProblem ? 'text-down' : 'text-content-400')}>
              {secondsProblem ?? (Number.isFinite(expiry) && expiry > 0 ? formatExpiry(expiry) : '')}
            </p>
          </>
        )}
      </td>
      <td className="align-top">
        <div className="relative">
          <input
            ref={factor.ref}
            name={factorName}
            aria-label={'Factor for ' + (seconds.typed || 'the new expiry') + ' s'}
            type="number"
            step="any"
            inputMode="decimal"
            defaultValue={initialFactor}
            data-published={row.published ? initialFactor : undefined}
            disabled={disabled}
            onChange={(event) => factor.setTyped(event.target.value)}
            className={clsx('field no-spin py-1.5 pr-8 tnum', factorProblem ? 'border-down/70' : null)}
          />
          <UnitSuffix unit="×" />
        </div>
        {factorProblem ? <p className="mt-1 text-[11px] leading-4 text-down">{factorProblem}</p> : null}
      </td>
      <td className={clsx('align-top text-xs text-content-400', TEXT_BESIDE_INPUT)}>
        {spec ? spec.factor + '×' : 'not in the defaults (1×)'}
        {row.published && factor.typed.trim() !== initialFactor ? (
          <span className="block text-accent">was {initialFactor}×</span>
        ) : null}
      </td>
      {disabled ? null : (
        <td className="align-top">
          <button
            type="button"
            className="grid h-8 w-8 place-items-center rounded-lg text-content-400 transition hover:bg-surface-800 hover:text-down"
            aria-label={'Remove the ' + (row.published ? formatExpiry(row.published.expirySeconds) : 'new') + ' factor'}
            onClick={onRemove}
          >
            <Trash2 className="h-4 w-4" />
          </button>
        </td>
      )}
    </tr>
  );
}

// ── instrument rules ────────────────────────────────────────────────────

/**
 * One card per instrument: BTC/USD and ETH/USD (priced at launch), then
 * EUR/USD, GBP/USD and XAU/USD (not offered at launch, still editable), then
 * any other symbol that has a rule. An instrument without a rule is refused
 * every quote; a rule can be created for it here (from the forex template,
 * switched off) and removed again before it is published.
 */
function SymbolRules({
  rules,
  disabled,
  onRowsChange,
}: {
  rules: PricingSymbolRule[];
  disabled: boolean;
  onRowsChange: () => void;
}) {
  const can = useAuthStore((s) => s.can);
  // The asset's own stake limits and page, as context (assets:read).
  const assets = useQuery({ queryKey: ['assets'], queryFn: api.assets.list, enabled: can('assets:read') });
  const bySymbol = new Map((assets.data ?? []).map((asset) => [asset.symbol, asset]));
  return (
    <div className="grid gap-4 p-4 xl:grid-cols-2 xl:items-start">
      {shownSymbols(rules).map((symbol) => (
        <SymbolRuleCard
          key={symbol}
          symbol={symbol}
          published={rules.find((rule) => rule.symbol === symbol) ?? null}
          asset={bySymbol.get(symbol) ?? null}
          disabled={disabled}
          onRowsChange={onRowsChange}
        />
      ))}
    </div>
  );
}

function SymbolRuleCard({
  symbol,
  published,
  asset,
  disabled,
  onRowsChange,
}: {
  symbol: string;
  published: PricingSymbolRule | null;
  asset: AdminAsset | null;
  disabled: boolean;
  onRowsChange: () => void;
}) {
  const [created, setCreated] = useState<PricingSymbolRule | null>(null);
  const firstRender = useRef(true);
  useEffect(() => {
    if (firstRender.current) {
      firstRender.current = false;
      return;
    }
    onRowsChange();
  }, [created, onRowsChange]);

  const rule = published ?? created;
  const atLaunch = LAUNCH_SYMBOLS.includes(symbol);
  const spec = SPEC_SYMBOL_RULES[symbol] ?? null;

  return (
    <details open={atLaunch} className="group/rule min-w-0 rounded-lg border border-surface-700 bg-surface-850/40">
      <summary className="flex cursor-pointer list-none flex-wrap items-center gap-2 px-3 py-2.5 [&::-webkit-details-marker]:hidden">
        <span className="text-sm font-semibold text-content-100">{symbol}</span>
        {atLaunch ? null : <Badge tone="warn">not offered at launch</Badge>}
        {rule ? (
          rule.enabled ? (
            <Badge tone="up">priced</Badge>
          ) : (
            <Badge>off</Badge>
          )
        ) : (
          <Badge>no rule</Badge>
        )}
        {rule ? (
          <span className="text-[11px] text-content-400 tnum">
            base {toDisplay(rule.basePayout, 'percent')}% · {toDisplay(rule.minPayout, 'percent')}–
            {toDisplay(rule.maxPayout, 'percent')}%
          </span>
        ) : null}
        <ChevronDown className="ml-auto h-4 w-4 shrink-0 text-content-400 transition group-open/rule:rotate-180" />
      </summary>

      <div className="border-t border-surface-700">
        {asset ? (
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2 text-[11px] text-content-400">
            <span>
              Asset stake limits {formatRupees(fromMinor(asset.minStake))} – {formatRupees(fromMinor(asset.maxStake))}
            </span>
            <Link to={'/admin/assets/' + asset.id} className="inline-flex items-center gap-1 font-medium text-accent">
              Per-expiry payouts
              <ArrowUpRight className="h-3 w-3" />
            </Link>
          </div>
        ) : null}

        {rule ? (
          <RuleTable symbol={symbol} rule={rule} isNew={!published} spec={spec} disabled={disabled} />
        ) : (
          <div className="px-3 py-3 text-xs text-content-400">
            No pricing rule: every quote on {symbol} is refused (NOT_PRICED).
          </div>
        )}

        {!published && !disabled ? (
          <div className="flex flex-wrap items-center gap-3 border-t border-surface-700 px-3 py-2.5">
            {created ? (
              <button type="button" className="btn-ghost px-3 py-1.5 text-xs" onClick={() => setCreated(null)}>
                <Trash2 className="h-3.5 w-3.5" />
                Remove this new rule
              </button>
            ) : (
              <button
                type="button"
                className="btn-ghost px-3 py-1.5 text-xs"
                onClick={() => setCreated(ruleTemplate(symbol))}
              >
                <Plus className="h-3.5 w-3.5" />
                Create a rule
              </button>
            )}
            <span className="text-[11px] text-content-400">
              {created
                ? 'Starts from the forex terms, switched off; not calibrated for this instrument.'
                : 'Starts switched off, from the forex terms.'}
            </span>
          </div>
        ) : null}
      </div>
    </details>
  );
}

function RuleTable({
  symbol,
  rule,
  isNew,
  spec,
  disabled,
}: {
  symbol: string;
  rule: PricingSymbolRule;
  /** Not yet published: its fields carry no published value and count as one change. */
  isNew: boolean;
  spec: PricingSymbolRule | null;
  disabled: boolean;
}) {
  const enabledName = ruleInput(symbol, 'enabled');
  const reasonName = ruleInput(symbol, 'disabledReason');
  // What is typed, for the checks that span two fields.
  const [typed, setTyped] = useState<Partial<Record<RuleNumberKey, string>>>(() =>
    Object.fromEntries(RULE_FIELDS.map((field) => [field.key, String(toDisplay(rule[field.key] ?? 0, field.unit))])),
  );
  const shown = (key: RuleNumberKey) => (typed[key]?.trim() ? Number(typed[key]) : NaN);
  const [base, floor, ceiling, minStake, maxStake] = (
    ['basePayout', 'minPayout', 'maxPayout', 'minStake', 'maxStake'] as const
  ).map(shown);
  const cross: Partial<Record<RuleNumberKey, string>> = {};
  if ([base, floor, ceiling].every(Number.isFinite) && !(floor <= base && base <= ceiling)) {
    cross.basePayout = 'Must lie between the floor and the ceiling';
  }
  if (minStake > 0 && maxStake > 0 && minStake > maxStake) cross.minStake = 'Above the maximum stake';

  return (
    <>
      <input type="hidden" name={ruleInput(symbol, 'present')} value="1" />
      {isNew ? <input type="hidden" data-published="absent" value="added" /> : null}
      <table className="table">
        <thead>
          <tr>
            <th>Parameter</th>
            <th className="w-44">Value</th>
            <th className="hidden sm:table-cell">Default</th>
          </tr>
        </thead>
        <tbody>
          <tr>
            <td className="align-top">
              <label htmlFor={enabledName} className="text-xs font-medium text-content-200">
                Offered
              </label>
              <p className="text-[11px] leading-4 text-content-400">Quotes and trades; off refuses every quote.</p>
            </td>
            <td className="align-top">
              <label className="inline-flex items-center gap-2 text-xs text-content-200">
                <input
                  id={enabledName}
                  type="checkbox"
                  className="h-4 w-4"
                  name={enabledName}
                  defaultChecked={rule.enabled}
                  data-published={isNew ? undefined : rule.enabled ? 'on' : 'off'}
                  disabled={disabled}
                />
                Priced and offered
              </label>
            </td>
            <td className="hidden align-top text-xs text-content-400 sm:table-cell">
              {spec ? (spec.enabled ? 'on' : 'off') : 'no rule'}
            </td>
          </tr>
          <tr>
            <td className="align-top">
              <label htmlFor={reasonName} className="text-xs font-medium text-content-200">
                Reason when off
              </label>
              <p className="text-[11px] leading-4 text-content-400">Shown with every refused quote.</p>
            </td>
            <td className="align-top" colSpan={2}>
              <input
                id={reasonName}
                name={reasonName}
                className="field py-1.5 text-xs"
                defaultValue={rule.disabledReason}
                data-published={isNew ? undefined : rule.disabledReason}
                maxLength={200}
                disabled={disabled}
              />
            </td>
          </tr>
          {RULE_FIELDS.map((field) => (
            <RuleNumberRow
              key={field.key}
              symbol={symbol}
              field={field}
              value={rule[field.key] ?? 0}
              spec={spec ? (spec[field.key] ?? 0) : null}
              isNew={isNew}
              disabled={disabled}
              crossProblem={cross[field.key] ?? null}
              onTyped={(value) => setTyped((current) => ({ ...current, [field.key]: value }))}
            />
          ))}
        </tbody>
      </table>
    </>
  );
}

function RuleNumberRow({
  symbol,
  field,
  value,
  spec,
  isNew,
  disabled,
  crossProblem,
  onTyped,
}: {
  symbol: string;
  field: RuleFieldSpec;
  value: number;
  spec: number | null;
  isNew: boolean;
  disabled: boolean;
  /** A rule that spans two fields (floor <= base <= ceiling, min <= max stake). */
  crossProblem: string | null;
  onTyped: (value: string) => void;
}) {
  const name = ruleInput(symbol, field.key);
  const initial = String(toDisplay(value, field.unit));
  const { ref, typed, setTyped } = useTyped(initial);
  const problem = checkValue(field, typed) ?? crossProblem;
  const current = Number(typed);
  const money = field.unit === 'rupees';
  const suffix = money ? '' : PRICING_UNITS[field.unit].suffix;
  const readout = !problem ? ruleReadout(field.unit, current, field.zero) : '';

  return (
    <tr>
      <td className="align-top">
        <label htmlFor={name} className="text-xs font-medium text-content-200">
          {field.label}
        </label>
        <p className="text-[11px] leading-4 text-content-400">{field.help}</p>
      </td>
      <td className="align-top">
        <div className="relative">
          {money ? (
            <span className="pointer-events-none absolute inset-y-0 left-3 flex items-center text-xs font-semibold text-content-300">
              ₹
            </span>
          ) : null}
          <input
            ref={ref}
            id={name}
            name={name}
            type="number"
            step="any"
            inputMode="decimal"
            defaultValue={initial}
            data-published={isNew ? undefined : initial}
            disabled={disabled}
            aria-invalid={problem ? true : undefined}
            onChange={(event) => {
              setTyped(event.target.value);
              onTyped(event.target.value);
            }}
            className={clsx(
              'field no-spin py-1.5 tnum',
              money ? 'pl-7' : unitPadding(suffix),
              problem ? 'border-down/70' : null,
            )}
          />
          <UnitSuffix unit={suffix} />
        </div>
        {problem || readout ? (
          <p className={clsx('mt-1 text-[11px] leading-4', problem ? 'font-medium text-down' : 'text-content-400')}>
            {problem ?? readout}
          </p>
        ) : null}
      </td>
      <td className={clsx('hidden align-top text-xs text-content-400 sm:table-cell', TEXT_BESIDE_INPUT)}>
        {spec === null ? '-' : specText(field, spec)}
        {!isNew && typed.trim() !== initial ? (
          <span className="block text-accent">was {specText(field, value)}</span>
        ) : null}
      </td>
    </tr>
  );
}

function specText(field: RuleFieldSpec, apiValue: number): string {
  if (field.zero && apiValue === 0) return 'no limit';
  return withUnit(toDisplay(apiValue, field.unit), field.unit);
}

/** The line under a rule value: rupees with grouping and in words, volatility as an annual figure. */
function ruleReadout(unit: PricingUnit, shown: number, zero?: string): string {
  if (!Number.isFinite(shown)) return '';
  if (unit === 'rupees') {
    if (zero && shown === 0) return 'Off - ' + zero;
    const words = rupeesInWords(shown);
    return formatRupees(shown) + (words ? ' · ' + words : '');
  }
  if (unit === 'vol' && shown > 0) {
    return '≈ ' + annualisedVolPercent(toApi(shown, 'vol')).toFixed(1) + '% a year';
  }
  return '';
}
