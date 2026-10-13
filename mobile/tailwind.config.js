/** @type {import('tailwindcss').Config} */

/*
 * Theme tokens - the same palette as the web client.
 *
 * Every colour resolves through a CSS variable holding space-separated RGB
 * channels, so opacity modifiers keep working (`bg-accent/15`) while the value
 * underneath swaps between themes. The variables are set at the root of the
 * app by `ThemeRoot` (src/theme), from the tables in src/theme/tokens.ts.
 *
 *   surface-*  page, panels, inputs, borders   (950 = furthest back)
 *   content-*  text                            (100 = strongest)
 *
 * Both ramps invert between themes, so no component carries a `dark:` variant.
 */
const token = (name) => `rgb(var(--${name}) / <alpha-value>)`;

module.exports = {
  content: ['./src/**/*.{ts,tsx}'],
  presets: [require('nativewind/preset')],
  theme: {
    extend: {
      colors: {
        surface: {
          950: token('surface-950'),
          900: token('surface-900'),
          850: token('surface-850'),
          800: token('surface-800'),
          700: token('surface-700'),
          600: token('surface-600'),
          500: token('surface-500'),
        },
        content: {
          100: token('content-100'),
          200: token('content-200'),
          300: token('content-300'),
          400: token('content-400'),
        },
        accent: { DEFAULT: token('accent'), soft: token('accent-soft'), solid: token('accent-solid') },
        up: { DEFAULT: token('up'), soft: token('up-soft'), solid: token('up-solid'), bright: token('up-bright') },
        down: {
          DEFAULT: token('down'),
          soft: token('down-soft'),
          solid: token('down-solid'),
          bright: token('down-bright'),
        },
        brand: token('brand'),
        scrim: token('scrim'),
        warn: token('warn'),
        critical: token('critical'),
      },
      borderRadius: { xl: '12px', '2xl': '16px', '3xl': '24px' },
    },
  },
  plugins: [],
};
