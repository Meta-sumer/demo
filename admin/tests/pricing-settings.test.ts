import { describe, expect, it } from 'vitest';
import {
  PRICING_FIELDS,
  RULE_FIELDS,
  SPEC_SYMBOL_RULES,
  annualisedVolPercent,
  buildPricingPatch,
  checkPricingForm,
  checkValue,
  expiryInput,
  ruleInput,
  shiftDecimal,
  toApi,
  toDisplay,
  withUnit,
} from '../src/utils/pricingSettings';

/**
 * The Settings page shows the pricing engine's fractions as an operator
 * thinks of them - "6 points", "86%", "4.45 x10^-5", rupees - and must send
 * back exactly what the API expects: the same doubles, not 0.07000000000000001.
 */
describe('shiftDecimal', () => {
  it('moves the decimal point without floating-point error', () => {
    // The multiply this replaces gets these wrong.
    expect(0.07 * 100).not.toBe(7);
    expect(shiftDecimal(0.07, 2)).toBe(7);
    expect(shiftDecimal(7, -2)).toBe(0.07);
    expect(shiftDecimal(0.035, 2)).toBe(3.5);
    expect(shiftDecimal(3.5, -2)).toBe(0.035);
    expect(shiftDecimal(0.86, 2)).toBe(86);
    expect(shiftDecimal(86, -2)).toBe(0.86);
    expect(shiftDecimal(0.005, 2)).toBe(0.5);
    expect(shiftDecimal(0.5, -2)).toBe(0.005);
  });

  it('handles exponent notation and tiny volatilities', () => {
    expect(shiftDecimal(4.45e-5, 5)).toBe(4.45);
    expect(shiftDecimal(4.45, -5)).toBe(4.45e-5);
    expect(shiftDecimal(6.78e-5, 5)).toBe(6.78);
    expect(shiftDecimal(1.25, -5)).toBe(1.25e-5);
    expect(shiftDecimal(1e-9, 5)).toBe(0.0001);
    expect(shiftDecimal(1e21, 2)).toBe(1e23);
    expect(shiftDecimal(-0.03, 2)).toBe(-3);
  });

  it('leaves 0 and non-numbers alone', () => {
    expect(shiftDecimal(0, 2)).toBe(0);
    expect(shiftDecimal(12.5, 0)).toBe(12.5);
    expect(Number.isNaN(shiftDecimal(NaN, 2))).toBe(true);
  });
});

describe('operator units', () => {
  it('shows payout terms in points and percent, volatility in 10^-5, money in rupees', () => {
    expect(toDisplay(0.06, 'points')).toBe(6); // k_vol
    expect(toDisplay(0.12, 'points')).toBe(12);
    expect(toDisplay(0.1, 'points')).toBe(10);
    expect(toDisplay(0.005, 'points')).toBe(0.5); // payout step
    expect(toDisplay(0.86, 'percent')).toBe(86); // base payout
    expect(toDisplay(0.65, 'percent')).toBe(65);
    expect(toDisplay(4.45e-5, 'vol')).toBe(4.45);
    expect(toDisplay(125_000_000, 'rupees')).toBe(1_250_000); // max exposure, paise -> rupees
    expect(toDisplay(2000, 'ms')).toBe(2000);
    expect(toDisplay(1, 'apiPercent')).toBe(1); // already percent on the wire
  });

  it('sends exactly what the API expects', () => {
    expect(toApi(6, 'points')).toBe(0.06);
    expect(toApi(7, 'points')).toBe(0.07);
    expect(toApi(3.5, 'points')).toBe(0.035);
    expect(toApi(0.5, 'points')).toBe(0.005);
    expect(toApi(86, 'percent')).toBe(0.86);
    expect(toApi(87.5, 'percent')).toBe(0.875);
    expect(toApi(4.45, 'vol')).toBe(4.45e-5);
    expect(toApi(1_250_000, 'rupees')).toBe(125_000_000);
    expect(toApi(10.5, 'rupees')).toBe(1050);
    expect(toApi(1.3, 'times')).toBe(1.3);
  });

  it('round-trips every specification default bit for bit', () => {
    for (const spec of PRICING_FIELDS) {
      expect(toApi(toDisplay(spec.spec, spec.unit), spec.unit)).toBe(spec.spec);
    }
    for (const rule of Object.values(SPEC_SYMBOL_RULES)) {
      for (const field of RULE_FIELDS) {
        const value = rule[field.key] ?? 0;
        expect(toApi(toDisplay(value, field.unit), field.unit)).toBe(value);
      }
    }
  });

  it('labels values with their unit', () => {
    expect(withUnit(6, 'points')).toBe('6 pts');
    expect(withUnit(86, 'percent')).toBe('86%');
    expect(withUnit(4, 'times')).toBe('4×');
    expect(withUnit(600_000, 'ms')).toBe('6,00,000 ms');
    expect(withUnit(1_250_000, 'rupees')).toBe('₹12,50,000');
    expect(withUnit(0.97, 'plain')).toBe('0.97');
  });

  it('reads the calibrated baseline as an annual figure', () => {
    expect(annualisedVolPercent(4.45e-5)).toBeCloseTo(24.99, 2);
    expect(annualisedVolPercent(6.78e-5)).toBeCloseTo(38.07, 2);
  });
});

function formValues(entries: Array<[string, string]>): Map<string, string> {
  return new Map(entries);
}

/** What the form holds for the defaults, as the page renders them. */
function defaultForm(): Array<[string, string]> {
  const entries: Array<[string, string]> = [
    ['pricingMode', 'DYNAMIC'],
    ['pricingRuleLabel', 'rules-2026.10.1'],
    ['pricingHedgeUnavailableAction', 'WIDEN'],
    ['pricingGuardEnabled', 'on'],
  ];
  for (const spec of PRICING_FIELDS) entries.push([spec.key, String(toDisplay(spec.spec, spec.unit))]);
  entries.push([expiryInput('a', 'seconds'), '60'], [expiryInput('a', 'factor'), '1']);
  entries.push([expiryInput('b', 'seconds'), '30'], [expiryInput('b', 'factor'), '1.3']);
  for (const symbol of ['BTC/USD', 'EUR/USD']) {
    const rule = SPEC_SYMBOL_RULES[symbol];
    entries.push([ruleInput(symbol, 'present'), '1']);
    if (rule.enabled) entries.push([ruleInput(symbol, 'enabled'), 'on']);
    entries.push([ruleInput(symbol, 'disabledReason'), rule.disabledReason]);
    for (const field of RULE_FIELDS) {
      entries.push([ruleInput(symbol, field.key), String(toDisplay(rule[field.key] ?? 0, field.unit))]);
    }
  }
  return entries;
}

describe('buildPricingPatch', () => {
  it('sends the defaults back unchanged, in API units', () => {
    const patch = buildPricingPatch(formValues(defaultForm()));
    expect(patch.pricingKVol).toBe(0.06);
    expect(patch.pricingMaxVolAdj).toBe(0.12);
    expect(patch.pricingPayoutStep).toBe(0.005);
    expect(patch.pricingHedgeUnavailableAdj).toBe(0.03);
    expect(patch.pricingStaleAfterMs).toBe(2000);
    expect(patch.pricingEwmaLambda).toBe(0.97);
    expect(patch.pricingGuardMinEdgePercent).toBe(1);
    expect(patch.earlyCloseMinProbabilityPercent).toBe(0.05);
    expect(patch.pricingGuardEnabled).toBe(true);
    expect(patch.pricingGuardConservative).toBe(false);
    expect(patch.pricingMode).toBe('DYNAMIC');
    expect(patch.pricingHedgeUnavailableAction).toBe('WIDEN');
    // Sorted by expiry.
    expect(patch.pricingExpiryFactors).toEqual([
      { expirySeconds: 30, factor: 1.3 },
      { expirySeconds: 60, factor: 1 },
    ]);
    expect(patch.pricingSymbolRules).toEqual([SPEC_SYMBOL_RULES['BTC/USD'], SPEC_SYMBOL_RULES['EUR/USD']]);
  });

  it('converts what the operator typed', () => {
    const values = formValues(defaultForm());
    values.set('pricingKVol', '7');
    values.set('pricingMaxExposureAdj', '12.5');
    values.set('pricingPayoutStep', '1');
    values.set(ruleInput('BTC/USD', 'basePayout'), '87');
    values.set(ruleInput('BTC/USD', 'hedgeCost'), '3');
    values.set(ruleInput('BTC/USD', 'baselineVol'), '5.1');
    values.set(ruleInput('BTC/USD', 'maxExposure'), '1000000');
    values.set(ruleInput('BTC/USD', 'minStake'), '50');
    values.set(ruleInput('BTC/USD', 'maxStake'), '25000.5');
    values.set(expiryInput('b', 'factor'), '1.25');
    values.delete('pricingGuardEnabled');
    const patch = buildPricingPatch(values);
    expect(patch.pricingKVol).toBe(0.07);
    expect(patch.pricingMaxExposureAdj).toBe(0.125);
    expect(patch.pricingPayoutStep).toBe(0.01);
    expect(patch.pricingGuardEnabled).toBe(false);
    expect(patch.pricingExpiryFactors).toEqual([
      { expirySeconds: 30, factor: 1.25 },
      { expirySeconds: 60, factor: 1 },
    ]);
    const [btc] = patch.pricingSymbolRules as Array<Record<string, number>>;
    expect(btc.basePayout).toBe(0.87);
    expect(btc.hedgeCost).toBe(0.03);
    expect(btc.baselineVol).toBe(5.1e-5);
    expect(btc.maxExposure).toBe(100_000_000);
    expect(btc.minStake).toBe(5000);
    expect(btc.maxStake).toBe(2_500_050);
  });

  it('sends an empty factor table when every row is removed', () => {
    const values = formValues(defaultForm().filter(([name]) => !name.startsWith('expiry:')));
    expect(buildPricingPatch(values).pricingExpiryFactors).toEqual([]);
  });
});

describe('checkPricingForm', () => {
  it('accepts the defaults', () => {
    expect(checkPricingForm(formValues(defaultForm()))).toEqual([]);
  });

  it('names each refused value in the operator’s units', () => {
    const values = formValues(defaultForm());
    values.set('pricingEwmaLambda', '1');
    values.set('pricingOfferTtlMs', '200');
    values.set('pricingVolBlockRatio', '1');
    values.set('pricingJumpConfirmations', '2.5');
    values.set('pricingMaxSpreadBps', '0');
    values.set(ruleInput('BTC/USD', 'basePayout'), '90');
    values.set(ruleInput('BTC/USD', 'maxPayout'), '101');
    values.set(ruleInput('BTC/USD', 'minPayout'), '65.2');
    values.set(ruleInput('BTC/USD', 'hedgeCost'), '-1');
    values.set(ruleInput('BTC/USD', 'minStake'), '500');
    values.set(ruleInput('BTC/USD', 'maxStake'), '100');
    values.set(expiryInput('b', 'seconds'), '60');
    values.set(expiryInput('a', 'factor'), '0');
    const messages = checkPricingForm(values).map((issue) => issue.message);
    expect(messages).toHaveLength(11);
    expect(messages).toContain('Volatility memory (EWMA λ): Must be between 0.5 and 0.9999');
    expect(messages).toContain('Offer valid for: Must be between 500 ms and 60,000 ms');
    expect(messages).toContain('Stop quoting above: Must be above 1× and at most 100×');
    expect(messages).toContain('Quotes that confirm a jump: Whole numbers only');
    expect(messages).toContain('Widest bid-ask spread: Must be above 0 bps and at most 1,000 bps');
    expect(messages).toContain('BTC/USD ceiling: Must be between 0.5% and 100%');
    expect(messages).toContain('BTC/USD hedge cost: Must be between 0 pts and 100 pts');
    expect(messages).toContain('BTC/USD: the floor must be a multiple of the 0.5-point payout step');
    expect(messages).toContain('BTC/USD: the minimum stake is above the maximum');
    expect(messages).toContain('Expiry factors: 60 s is listed twice');
    expect(messages).toContain('Expiry factor for 60 s: Must be between 0.01× and 10×');
  });

  it('checks floor ≤ base ≤ ceiling', () => {
    const values = formValues(defaultForm());
    values.set(ruleInput('BTC/USD', 'basePayout'), '90');
    expect(checkPricingForm(values).map((issue) => issue.message)).toEqual([
      'BTC/USD: the payouts need floor ≤ base ≤ ceiling',
    ]);
  });
});

describe('checkValue', () => {
  it('reads 0 as allowed where 0 means something', () => {
    expect(checkValue({ min: 0, max: 60_000, integer: true, unit: 'ms' }, '0')).toBeNull();
    expect(checkValue({ min: 0, max: Infinity, unit: 'rupees' }, '0')).toBeNull();
  });

  it('refuses empty, non-numeric and sub-paisa values', () => {
    expect(checkValue({ min: 0, max: 100, unit: 'points' }, '')).toBe('Enter a value');
    expect(checkValue({ min: 0, max: 100, unit: 'points' }, 'six')).toBe('Enter a number');
    expect(checkValue({ min: 0, max: Infinity, unit: 'rupees' }, '10.005')).toBe('At most two decimals (paise)');
    expect(checkValue({ min: 0, max: Infinity, above: true, unit: 'rupees' }, '0')).toBe('Must be above ₹0');
  });
});
