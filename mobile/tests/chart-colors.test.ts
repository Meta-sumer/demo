import { describe, expect, it } from 'vitest';
import { channelsToHex, channelsToRgba, withAlpha } from '../src/utils/theme-colors';

/**
 * Chart colour conversion.
 *
 * Theme tokens are stored as space-separated RGB channels so Tailwind can apply
 * an alpha to them. `rgb(100 108 126)` built from those channels is valid CSS
 * Color Level 4 - and lightweight-charts throws "Cannot parse color" on it,
 * taking the whole chart down. Anything handed to a charting library therefore
 * has to be hex or comma-separated rgba().
 */
describe('channelsToHex', () => {
  it('converts stored channels to hex', () => {
    expect(channelsToHex('100 108 126')).toBe('#646c7e');
    expect(channelsToHex('255 255 255')).toBe('#ffffff');
    expect(channelsToHex('8 9 13')).toBe('#08090d');
  });

  it('pads single-digit channels', () => {
    expect(channelsToHex('0 0 0')).toBe('#000000');
    expect(channelsToHex('1 2 3')).toBe('#010203');
  });

  it('tolerates the comma-separated form and extra whitespace', () => {
    expect(channelsToHex('  100,108 , 126 ')).toBe('#646c7e');
  });

  it('never emits the space-separated syntax that breaks the chart library', () => {
    const output = channelsToHex('100 108 126')!;
    expect(output).not.toMatch(/\s/);
    expect(output.startsWith('#')).toBe(true);
  });

  it('returns null rather than a broken colour string', () => {
    expect(channelsToHex('')).toBeNull();
    expect(channelsToHex('100 108')).toBeNull();
    expect(channelsToHex('not a colour')).toBeNull();
  });

  it('clamps out-of-range channels', () => {
    expect(channelsToHex('300 -20 128')).toBe('#ff0080');
  });
});

describe('channelsToRgba', () => {
  it('emits the comma-separated form every parser understands', () => {
    expect(channelsToRgba('40 46 62', 0.55)).toBe('rgba(40, 46, 62, 0.55)');
  });

  it('returns null for unparseable input', () => {
    expect(channelsToRgba('nope', 0.5)).toBeNull();
  });
});

describe('withAlpha', () => {
  it('converts hex to the comma-separated rgba form', () => {
    expect(withAlpha('#22c55e', 0.5)).toBe('rgba(34, 197, 94, 0.5)');
  });

  it('expands three-digit hex', () => {
    expect(withAlpha('#0f8', 1)).toBe('rgba(0, 255, 136, 1)');
  });

  it('multiplies into an existing alpha rather than replacing it', () => {
    // The grid token already carries 0.55; a 40% opacity setting must dim it
    // further, not brighten it back up to 0.4 of full.
    expect(withAlpha('rgba(40, 46, 62, 0.5)', 0.4)).toBe('rgba(40, 46, 62, 0.2)');
  });

  it('clamps the requested alpha', () => {
    expect(withAlpha('#000000', 5)).toBe('rgba(0, 0, 0, 1)');
    expect(withAlpha('#000000', -2)).toBe('rgba(0, 0, 0, 0)');
  });

  it('never emits the space-separated syntax lightweight-charts cannot parse', () => {
    const outputs = [
      withAlpha('#22c55e', 0.3),
      withAlpha('#0f8', 0.9),
      withAlpha('rgba(1, 2, 3, 0.8)', 0.5),
    ];

    for (const value of outputs) {
      expect(value).not.toMatch(/rgba?\(\s*\d+\s+\d+/);
      expect(value).toMatch(/^rgba\(\d+, \d+, \d+, [\d.]+\)$/);
    }
  });

  it('passes through anything it does not recognise instead of corrupting it', () => {
    expect(withAlpha('transparent', 0.5)).toBe('transparent');
  });
});
