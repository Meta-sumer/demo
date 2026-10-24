import type { PricingExpiryFactor, PricingSymbolRule, RiskConfig } from '../types/api';
import { fromMinor, toMinor } from './format';

/**
 * The pricing engine's parameters as the Settings page edits them
 * (docs/audit/pricing/pricing-spec.md section 11, PUT /admin/settings/risk).
 *
 * The API speaks the engine's units: payout terms are fractions of the stake
 * (0.06 = 6 payout points), baseline volatility is a fraction per square-root
 * second and money is paise. An operator thinks in points, percent and
 * rupees, so every field is shown in an operator unit and converted back on
 * submit:
 *
 *   unit        shown as                        shown value -> API value
 *   points      payout points, "6 pts"          x 10^-2   (k_vol 6 -> 0.06)
 *   percent     percent of the stake, "86%"     x 10^-2   (base 86 -> 0.86)
 *   vol         units of 10^-5 per sqrt(s)      x 10^-5   (4.45 -> 4.45e-5)
 *   rupees      ₹, Indian grouping              x 10^2    (paise, toMinor)
 *   the rest    as the API (ms, bps, x, %, ...) x 1
 *
 * The conversions are decimal shifts of the number's shortest decimal text
 * (shiftDecimal), never a floating-point multiply: 0.07 * 100 is
 * 7.000000000000001, while shiftDecimal(0.07, 2) is 7 and shiftDecimal(7, -2)
 * is the very double the literal 0.07 is. So "6 points" typed is sent as
 * exactly 0.06, and a value read and left alone goes back bit for bit.
 */

/**
 * value x 10^places, computed on the decimal text: the double nearest the
 * shifted decimal, which is exact for every value an operator can type.
 */
export function shiftDecimal(value: number, places: number): number {
  if (places === 0 || !Number.isFinite(value)) return value;
  const [digits, exponent = '0'] = String(value).split('e');
  return Number(digits + 'e' + (Number(exponent) + places));
}

export type PricingUnit =
  | 'points'
  | 'percent'
  | 'vol'
  | 'rupees'
  | 'ms'
  | 'bps'
  | 'times'
  | 'quotes'
  | 'apiPoints'
  | 'apiPercent'
  | 'plain';

/** How a unit is drawn in its field, and the power of ten from the API's value to the shown one. */
export const PRICING_UNITS: Record<PricingUnit, { suffix: string; places: number }> = {
  points: { suffix: 'pts', places: 2 },
  percent: { suffix: '%', places: 2 },
  vol: { suffix: '×10⁻⁵', places: 5 },
  rupees: { suffix: '₹', places: -2 },
  ms: { suffix: 'ms', places: 0 },
  bps: { suffix: 'bps', places: 0 },
  times: { suffix: '×', places: 0 },
  quotes: { suffix: 'quotes', places: 0 },
  // Already payout points / percent on the wire.
  apiPoints: { suffix: 'pts', places: 0 },
  apiPercent: { suffix: '%', places: 0 },
  plain: { suffix: '', places: 0 },
};

/** The API's value as the field shows it. */
export function toDisplay(api: number, unit: PricingUnit): number {
  return unit === 'rupees' ? fromMinor(api) : shiftDecimal(api, PRICING_UNITS[unit].places);
}

/** A shown value as the API takes it (money: whole paise). */
export function toApi(shown: number, unit: PricingUnit): number {
  return unit === 'rupees' ? toMinor(shown) : shiftDecimal(shown, -PRICING_UNITS[unit].places);
}

/** A number for a message or a hint, with Indian grouping: 600000 -> "6,00,000". */
export function formatShown(value: number): string {
  return value.toLocaleString('en-IN', { maximumFractionDigits: 8 });
}

/** "6 pts", "86%", "₹1,25,00,000", "2,000 ms". */
export function withUnit(shown: number, unit: PricingUnit): string {
  const suffix = PRICING_UNITS[unit].suffix;
  if (unit === 'rupees') return '₹' + formatShown(shown);
  if (!suffix) return formatShown(shown);
  return formatShown(shown) + (suffix === '%' || suffix === '×' ? '' : ' ') + suffix;
}

export type PricingNumberKey =
  | 'pricingStaleAfterMs'
  | 'pricingMaxSpreadBps'
  | 'pricingMaxJumpBps'
  | 'pricingJumpConfirmations'
  | 'pricingPendingOutlierMaxAgeMs'
  | 'pricingEwmaLambda'
  | 'pricingVolSampleIntervalMs'
  | 'pricingKVol'
  | 'pricingMaxVolAdj'
  | 'pricingVolBlockRatio'
  | 'pricingKSpread'
  | 'pricingMaxSpreadAdj'
  | 'pricingKExposure'
  | 'pricingMaxExposureAdj'
  | 'pricingPayoutStep'
  | 'pricingOfferTtlMs'
  | 'pricingMaxSlippageBps'
  | 'pricingDisplayTolerancePercent'
  | 'pricingGuardMinEdgePercent'
  | 'pricingHedgeUnavailableAdj'
  | 'pricingHedgeTakerFeeBps'
  | 'pricingHedgeSpreadBps'
  | 'pricingHedgeSlippageBps'
  | 'earlyCloseMinProbabilityPercent';

/** A numeric pricing parameter: what the operator sees, and the bounds the server enforces. */
export interface PricingFieldSpec {
  key: PricingNumberKey;
  label: string;
  /** What it does, in one line. */
  help: string;
  unit: PricingUnit;
  /** The specification's default, in API units. */
  spec: number;
  /** Allowed range in shown units (the server's bounds, converted). */
  min: number;
  max: number;
  /** The minimum itself is refused ("above 0"). */
  above?: boolean;
  integer?: boolean;
  /** What 0 does, when it does something other than "nothing". */
  zero?: string;
}

export interface PricingGroupSpec {
  id: string;
  title: string;
  description: string;
  fields: PricingFieldSpec[];
}

type Bounds = Pick<PricingFieldSpec, 'min' | 'max' | 'above' | 'integer' | 'zero'>;

function field(key: PricingNumberKey, label: string, help: string, unit: PricingUnit, spec: number, bounds: Bounds) {
  return { key, label, help, unit, spec, ...bounds };
}

/** The numeric parameters, in the order the engine applies them. */
export const PRICING_GROUPS: PricingGroupSpec[] = [
  {
    id: 'market-data',
    title: 'Market data checks',
    description: 'Which Binance quotes the engine believes: stale, wide and jumping prices are held back.',
    fields: [
      field('pricingStaleAfterMs', 'Price is stale after', 'No offer and no acceptance on a price older than this.',
        'ms', 2000, { min: 100, max: 60_000, integer: true }),
      field('pricingMaxSpreadBps', 'Widest bid-ask spread', 'A quote with a wider spread is rejected as a bad print.',
        'bps', 8, { min: 0, max: 1000, above: true }),
      field('pricingMaxJumpBps', 'Jump held for confirmation',
        'A move larger than this is held until later quotes confirm it.',
        'bps', 150, { min: 1, max: 10_000 }),
      field('pricingJumpConfirmations', 'Quotes that confirm a jump',
        'Agreeing quotes needed before a jump is accepted.',
        'quotes', 3, { min: 1, max: 50, integer: true }),
      field('pricingPendingOutlierMaxAgeMs', 'Held jump expires after',
        'An unconfirmed jump older than this is dropped.',
        'ms', 5000, { min: 100, max: 600_000, integer: true }),
    ],
  },
  {
    id: 'volatility',
    title: 'Volatility',
    description: 'How volatility is measured, how many points it costs, and when quoting stops.',
    fields: [
      field('pricingEwmaLambda', 'Volatility memory (EWMA λ)',
        'Weight kept on past volatility at each observation; higher reacts more slowly.',
        'plain', 0.97, { min: 0.5, max: 0.9999 }),
      field('pricingVolSampleIntervalMs', 'One observation every',
        'Spacing between the quotes the volatility estimate uses.',
        'ms', 1000, { min: 0, max: 60_000, integer: true, zero: 'every accepted quote (the spec’s literal rule)' }),
      field('pricingKVol', 'Points per unit of extra volatility (k_vol)',
        'Off the payout per unit of volatility above normal, times the expiry factor.',
        'points', 0.06, { min: 0, max: 100 }),
      field('pricingMaxVolAdj', 'Volatility deduction cap', 'The volatility deduction never takes more than this.',
        'points', 0.12, { min: 0, max: 100 }),
      field('pricingVolBlockRatio', 'Stop quoting above', 'No offers while volatility exceeds this multiple of normal.',
        'times', 4, { min: 1, max: 100, above: true }),
    ],
  },
  {
    id: 'spread-exposure',
    title: 'Spread and exposure',
    description: 'Points off for a wide market and for a book loaded on one side.',
    fields: [
      field('pricingKSpread', 'Points per unit of spread (k_spread)',
        'Off the payout per unit of spread relative to the expected move.',
        'points', 0.02, { min: 0, max: 100 }),
      field('pricingMaxSpreadAdj', 'Spread deduction cap', 'The spread deduction never takes more than this.',
        'points', 0.05, { min: 0, max: 100 }),
      field('pricingKExposure', 'Points at a full side (k_exposure)',
        'Off the payout when a side uses its whole exposure limit; scales with use squared.',
        'points', 0.08, { min: 0, max: 100 }),
      field('pricingMaxExposureAdj', 'Exposure deduction cap', 'The exposure deduction never takes more than this.',
        'points', 0.1, { min: 0, max: 100 }),
    ],
  },
  {
    id: 'offers',
    title: 'Rounding and offers',
    description: 'How payouts are rounded, how long an offer lasts and how far the price may move before it is taken.',
    fields: [
      field('pricingPayoutStep', 'Payout step',
        'Payouts are rounded down to a multiple of this; floors and ceilings must sit on it.',
        'points', 0.005, { min: 0.01, max: 10 }),
      field('pricingOfferTtlMs', 'Offer valid for', 'How long a quoted payout can be accepted.',
        'ms', 3000, { min: 500, max: 60_000, integer: true }),
      field('pricingMaxSlippageBps', 'Price move allowed before accepting',
        'Accepting is refused, with a fresh quote, if the price moved more than this.',
        'bps', 2, { min: 0, max: 1000, zero: 'any move asks for a fresh quote' }),
      field('pricingDisplayTolerancePercent', 'Old-app payout tolerance',
        'Apps without offers: refused if the payout fell more than this below what they showed.',
        'apiPoints', 0, { min: 0, max: 5, zero: 'any lower payout asks for a fresh quote' }),
    ],
  },
  {
    id: 'guard',
    title: 'Safety guard',
    description: 'The calibrated win-probability check after the formula: it can refuse an offer, never raise one.',
    fields: [
      field('pricingGuardMinEdgePercent', 'Minimum expected edge',
        'Refuse an offer whose expected platform result is below this share of the stake.',
        'apiPercent', 1, { min: -10, max: 50 }),
    ],
  },
  {
    id: 'hedge',
    title: 'Hedge cost',
    description: 'Payouts while a needed hedge cannot be placed, and the Binance cost reported beside them.',
    fields: [
      field('pricingHedgeUnavailableAdj', 'Hedge outage deduction',
        'Extra points off while a needed hedge is unavailable (when set to widen).',
        'points', 0.03, { min: 0, max: 100 }),
      field('pricingHedgeTakerFeeBps', 'Venue taker fee', 'Binance futures taker fee per side; reported, not charged.',
        'bps', 5, { min: 0, max: 1000 }),
      field('pricingHedgeSpreadBps', 'Venue half-spread', 'Half the venue spread per side; reported, not charged.',
        'bps', 1, { min: 0, max: 1000 }),
      field('pricingHedgeSlippageBps', 'Venue slippage', 'Expected slippage per side; reported, not charged.',
        'bps', 2, { min: 0, max: 1000 }),
    ],
  },
  {
    id: 'sell-now',
    title: 'Sell-now pricing',
    description: 'The floor under the win probability a Sell-now price is computed from.',
    fields: [
      field('earlyCloseMinProbabilityPercent', 'Lowest win probability',
        'A losing position is never priced as if it had less chance than this.',
        'apiPercent', 0.05, { min: 0.01, max: 10 }),
    ],
  },
];

export const PRICING_FIELDS: PricingFieldSpec[] = PRICING_GROUPS.flatMap((group) => group.fields);

/** The non-numeric parameters and their specification defaults. */
export const PRICING_SPEC_CHOICES = {
  pricingMode: 'DYNAMIC',
  pricingRuleLabel: 'rules-2026.10.1',
  pricingHedgeUnavailableAction: 'WIDEN',
  pricingGuardEnabled: true,
  pricingGuardConservative: false,
} as const;

export const PRICING_TOGGLES = ['pricingGuardEnabled', 'pricingGuardConservative'] as const;

/** The specification's expiry factors; an expiry not listed uses 1. */
export const SPEC_EXPIRY_FACTORS: PricingExpiryFactor[] = [
  { expirySeconds: 30, factor: 1.3 },
  { expirySeconds: 60, factor: 1 },
  { expirySeconds: 120, factor: 0.9 },
  { expirySeconds: 300, factor: 0.8 },
];

export const EXPIRY_SECONDS_MAX = 86_400;
export const FACTOR_MIN = 0.01;
export const FACTOR_MAX = 10;

/** Instrument rule fields with an operator unit. */
export type RuleNumberKey =
  | 'basePayout'
  | 'minPayout'
  | 'maxPayout'
  | 'baselineVol'
  | 'hedgeCost'
  | 'margin'
  | 'maxExposure'
  | 'minStake'
  | 'maxStake';

export interface RuleFieldSpec {
  key: RuleNumberKey;
  label: string;
  help: string;
  unit: PricingUnit;
  min: number;
  max: number;
  above?: boolean;
  zero?: string;
}

function ruleField(key: RuleNumberKey, label: string, help: string, unit: PricingUnit, bounds: Omit<Bounds, 'integer'>) {
  return { key, label, help, unit, ...bounds };
}

/** An instrument's terms; payouts and the per-expiry overrides of each payout row are on the asset page. */
export const RULE_FIELDS: RuleFieldSpec[] = [
  ruleField('basePayout', 'Base payout', 'Starting payout before deductions; each expiry’s payout row sets its own.',
    'percent', { min: 1, max: 100 }),
  ruleField('minPayout', 'Floor', 'The payout never goes below this.',
    'percent', { min: 0.5, max: 100 }),
  ruleField('maxPayout', 'Ceiling', 'The payout never goes above this (at most 100%).',
    'percent', { min: 0.5, max: 100 }),
  ruleField('baselineVol', 'Normal volatility', 'Volatility per √s the engine treats as normal.',
    'vol', { min: 0, max: 10_000, above: true }),
  ruleField('hedgeCost', 'Hedge cost', 'Points off for hedging, times the expiry factor.',
    'points', { min: 0, max: 100 }),
  ruleField('margin', 'Margin', 'Platform margin off every payout.',
    'points', { min: 0, max: 100 }),
  ruleField('maxExposure', 'Max exposure per side', 'Largest net loss allowed if one side wins.',
    'rupees', { min: 0, max: Infinity, above: true }),
  ruleField('minStake', 'Minimum stake', 'Narrows the asset’s own stake limits.',
    'rupees', { min: 0, max: Infinity, zero: 'no limit of its own' }),
  ruleField('maxStake', 'Maximum stake', 'Narrows the asset’s own stake limits.',
    'rupees', { min: 0, max: Infinity, zero: 'no limit of its own' }),
];

/** Priced at launch; the others are shown as "not offered at launch". */
export const LAUNCH_SYMBOLS = ['BTC/USD', 'ETH/USD'];
/** Always shown, in this order, whether or not they have a rule. */
export const SHOWN_SYMBOLS = ['BTC/USD', 'ETH/USD', 'EUR/USD', 'GBP/USD', 'XAU/USD'];

const FOREX_REASON = 'out of launch scope: no live price source for this instrument';

function cryptoRule(symbol: string, baselineVol: number): PricingSymbolRule {
  return { symbol, enabled: true, disabledReason: '', basePayout: 0.86, minPayout: 0.65, maxPayout: 0.88, baselineVol,
    hedgeCost: 0.035, margin: 0.035, maxExposure: 125_000_000, minStake: 0, maxStake: 0 };
}

function forexRule(symbol: string): PricingSymbolRule {
  return { symbol, enabled: false, disabledReason: FOREX_REASON, basePayout: 0.9, minPayout: 0.7, maxPayout: 0.92,
    baselineVol: 1.25e-5, hedgeCost: 0.02, margin: 0.03, maxExposure: 250_000_000, minStake: 0, maxStake: 0 };
}

/** The specification's instrument rules (models.DefaultPricingSymbolRules); XAU/USD has none. */
export const SPEC_SYMBOL_RULES: Record<string, PricingSymbolRule> = {
  'BTC/USD': cryptoRule('BTC/USD', 4.45e-5),
  'ETH/USD': cryptoRule('ETH/USD', 6.78e-5),
  'EUR/USD': forexRule('EUR/USD'),
  'GBP/USD': forexRule('GBP/USD'),
};

/** A starting point for an instrument without a rule: the forex terms, not offered. */
export function ruleTemplate(symbol: string): PricingSymbolRule {
  return { ...forexRule(symbol), disabledReason: 'not offered at launch' };
}

/** Volatility per √s as the annual figure an operator recognises: 4.45e-5 -> about 25.0%. */
export function annualisedVolPercent(perSqrtSecond: number): number {
  return perSqrtSecond * Math.sqrt(365 * 24 * 3600) * 100;
}

/** The symbols the page shows: the five instruments, then any other rule's. */
export function shownSymbols(rules: PricingSymbolRule[]): string[] {
  const extra = rules.map((rule) => rule.symbol).filter((symbol) => !SHOWN_SYMBOLS.includes(symbol));
  return [...SHOWN_SYMBOLS, ...extra.sort()];
}

// ── form field names ────────────────────────────────────────────────────
// The Settings form is uncontrolled: FormData is read on submit. Scalar
// parameters use their API name; rules and expiry rows are addressed so the
// submit handler can rebuild the arrays.

export const ruleInput = (symbol: string, field: string) => 'rule:' + symbol + ':' + field;
export const expiryInput = (rowId: string, field: 'seconds' | 'factor') => 'expiry:' + rowId + ':' + field;

const RULE_PRESENT = /^rule:(.+):present$/;
const EXPIRY_ROW = /^expiry:([^:]+):seconds$/;

/**
 * The pricing part of PUT /admin/settings/risk from the form's values, in the
 * API's units. Every parameter is sent (unchanged ones too, as the rest of the
 * page does); a switch absent from the values is off, so this is only called
 * when the pricing card was rendered.
 */
export function buildPricingPatch(values: ReadonlyMap<string, string>): Record<string, unknown> {
  const patch: Record<string, unknown> = {};
  for (const key of ['pricingMode', 'pricingRuleLabel', 'pricingHedgeUnavailableAction'] as const) {
    const value = values.get(key);
    if (value !== undefined) patch[key] = value.trim();
  }
  for (const key of PRICING_TOGGLES) patch[key] = values.get(key) === 'on';
  for (const spec of PRICING_FIELDS) {
    const raw = values.get(spec.key);
    if (raw !== undefined) patch[spec.key] = toApi(Number(raw), spec.unit);
  }

  const factors: PricingExpiryFactor[] = [];
  for (const name of values.keys()) {
    const row = EXPIRY_ROW.exec(name);
    if (!row) continue;
    factors.push({
      expirySeconds: Number(values.get(name)),
      factor: Number(values.get(expiryInput(row[1], 'factor'))),
    });
  }
  patch.pricingExpiryFactors = factors.sort((a, b) => a.expirySeconds - b.expirySeconds);

  const rules: PricingSymbolRule[] = [];
  for (const name of values.keys()) {
    const present = RULE_PRESENT.exec(name);
    if (!present) continue;
    const symbol = present[1];
    const number = (key: RuleNumberKey) =>
      toApi(Number(values.get(ruleInput(symbol, key))), RULE_FIELDS.find((field) => field.key === key)!.unit);
    rules.push({
      symbol,
      enabled: values.get(ruleInput(symbol, 'enabled')) === 'on',
      disabledReason: (values.get(ruleInput(symbol, 'disabledReason')) ?? '').trim(),
      basePayout: number('basePayout'),
      minPayout: number('minPayout'),
      maxPayout: number('maxPayout'),
      baselineVol: number('baselineVol'),
      hedgeCost: number('hedgeCost'),
      margin: number('margin'),
      maxExposure: number('maxExposure'),
      minStake: number('minStake'),
      maxStake: number('maxStake'),
    });
  }
  patch.pricingSymbolRules = rules;
  return patch;
}

// ── checks before sending ───────────────────────────────────────────────
// The server is the authority; these name the field in the operator's units
// before a round trip, and are what the fields show under themselves.

export function rangeText(spec: { min: number; max: number; above?: boolean; unit: PricingUnit }): string {
  const max = Number.isFinite(spec.max) ? ' and at most ' + withUnit(spec.max, spec.unit) : '';
  if (spec.above) return 'Must be above ' + withUnit(spec.min, spec.unit) + max;
  if (!max) return 'Must be at least ' + withUnit(spec.min, spec.unit);
  return 'Must be between ' + withUnit(spec.min, spec.unit) + ' and ' + withUnit(spec.max, spec.unit);
}

/** Why a typed value is refused, or null. */
export function checkValue(
  spec: { min: number; max: number; above?: boolean; integer?: boolean; unit: PricingUnit },
  raw: string,
): string | null {
  const text = raw.trim();
  if (text === '') return 'Enter a value';
  const value = Number(text);
  if (!Number.isFinite(value)) return 'Enter a number';
  if (spec.integer && !Number.isInteger(value)) return 'Whole numbers only';
  if (spec.unit === 'rupees' && !Number.isInteger(shiftDecimal(value, 2))) return 'At most two decimals (paise)';
  if ((spec.above ? value <= spec.min : value < spec.min) || value > spec.max) return rangeText(spec);
  return null;
}

export interface PricingIssue {
  /** The form field to point at. */
  name: string;
  /** The operator's wording, with the field's label. */
  message: string;
}

/** Every problem in the pricing part of the form, in page order. */
export function checkPricingForm(values: ReadonlyMap<string, string>): PricingIssue[] {
  const issues: PricingIssue[] = [];
  for (const spec of PRICING_FIELDS) {
    const raw = values.get(spec.key);
    if (raw === undefined) continue;
    const problem = checkValue(spec, raw);
    if (problem) issues.push({ name: spec.key, message: spec.label + ': ' + problem });
  }
  const label = values.get('pricingRuleLabel');
  if (label !== undefined && (label.trim().length < 3 || label.trim().length > 60)) {
    issues.push({ name: 'pricingRuleLabel', message: 'Rule label: 3 to 60 characters' });
  }

  const seen = new Set<number>();
  for (const name of values.keys()) {
    const row = EXPIRY_ROW.exec(name);
    if (!row) continue;
    const raw = values.get(name) ?? '';
    const seconds = checkValue({ min: 1, max: EXPIRY_SECONDS_MAX, integer: true, unit: 'plain' }, raw);
    const expiry = Number(raw);
    if (seconds) issues.push({ name, message: 'Expiry factors, expiry in seconds: ' + seconds });
    else if (seen.has(expiry)) issues.push({ name, message: 'Expiry factors: ' + expiry + ' s is listed twice' });
    seen.add(expiry);
    const factorName = expiryInput(row[1], 'factor');
    const factor = checkValue({ min: FACTOR_MIN, max: FACTOR_MAX, unit: 'times' }, values.get(factorName) ?? '');
    if (factor) issues.push({ name: factorName, message: 'Expiry factor for ' + raw + ' s: ' + factor });
  }

  for (const name of values.keys()) {
    const present = RULE_PRESENT.exec(name);
    if (!present) continue;
    const symbol = present[1];
    const shown: Partial<Record<RuleNumberKey, number>> = {};
    for (const spec of RULE_FIELDS) {
      const field = ruleInput(symbol, spec.key);
      const raw = values.get(field) ?? '';
      const problem = checkValue(spec, raw);
      if (problem) issues.push({ name: field, message: symbol + ' ' + spec.label.toLowerCase() + ': ' + problem });
      else shown[spec.key] = Number(raw);
    }
    const issue = (key: string, message: string) =>
      issues.push({ name: ruleInput(symbol, key), message: symbol + ': ' + message });
    const { basePayout, minPayout, maxPayout, minStake, maxStake } = shown;
    if (basePayout !== undefined && minPayout !== undefined && maxPayout !== undefined &&
      !(minPayout <= basePayout && basePayout <= maxPayout)) {
      issue('basePayout', 'the payouts need floor ≤ base ≤ ceiling');
    }
    // A payout percent and payout points are the same number (1% of the
    // stake is one point), so the floor and ceiling sit on the step grid
    // when percent / step is whole.
    const step = Number(values.get('pricingPayoutStep'));
    for (const [key, value] of [['minPayout', minPayout], ['maxPayout', maxPayout]] as const) {
      if (value !== undefined && step > 0 && !onStep(value, step)) {
        const what = key === 'minPayout' ? 'the floor' : 'the ceiling';
        issue(key, what + ' must be a multiple of the ' + step + '-point payout step');
      }
    }
    if (minStake && maxStake && minStake > maxStake) issue('minStake', 'the minimum stake is above the maximum');
    if ((values.get(ruleInput(symbol, 'disabledReason')) ?? '').length > 200) {
      issue('disabledReason', 'the reason is limited to 200 characters');
    }
  }
  return issues;
}

/** Whether value is a whole number of steps (the server's tolerance). */
export function onStep(value: number, step: number): boolean {
  const steps = value / step;
  return Math.abs(steps - Math.round(steps)) < 1e-6;
}

/** The pricing fields a server sends; an older server sends none and the card is not shown. */
export function hasPricing(config: RiskConfig): boolean {
  return config.pricingMode !== undefined && config.pricingSymbolRules !== undefined;
}
