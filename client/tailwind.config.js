/** @type {import('tailwindcss').Config} */

/*
 * Theme tokens.
 *
 * Every colour resolves through a CSS custom property holding space-separated
 * RGB channels, which is what lets Tailwind's opacity modifiers keep working
 * (`bg-accent/15`) while the underlying value swaps between light and dark.
 *
 * The two ramps are semantic, not literal:
 *   surface-*  page, panels, inputs, borders   (950 = furthest back)
 *   content-*  text                            (100 = strongest)
 * Both invert between themes, so `bg-surface-950` is near-black in dark and
 * near-white in light without a single `dark:` variant in the components.
 */
const token = (name) => `rgb(var(--${name}) / <alpha-value>)`;

export default {
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  // Themes are driven by a data attribute set before first paint, so a `dark:`
  // variant (should one ever be needed) follows the same source of truth.
  darkMode: ['selector', '[data-theme="dark"]'],
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
        up: {
          DEFAULT: token('up'),
          soft: token('up-soft'),
          solid: token('up-solid'),
          bright: token('up-bright'),
        },
        down: {
          DEFAULT: token('down'),
          soft: token('down-soft'),
          solid: token('down-solid'),
          bright: token('down-bright'),
        },
        // Order amount tags and active-order indicators.
        brand: token('brand'),
        // Modal veil - dark in both themes, unlike every other surface.
        scrim: 'rgb(var(--scrim) / <alpha-value>)',
        warn: token('warn'),
        critical: token('critical'),
      },
      fontFamily: {
        sans: ['Inter var', 'Inter', 'ui-sans-serif', 'system-ui', '-apple-system', 'Segoe UI', 'Roboto', 'sans-serif'],
        mono: ['ui-monospace', 'SFMono-Regular', 'Menlo', 'Consolas', 'monospace'],
      },
      boxShadow: {
        // Elevation reads differently per theme: dark leans on a light inset
        // edge, light on a real cast shadow. Both come from the same token.
        panel: 'var(--shadow-panel)',
        raised: 'var(--shadow-raised)',
        overlay: 'var(--shadow-overlay)',
        'up-glow': 'var(--shadow-up)',
        'down-glow': 'var(--shadow-down)',
      },
      borderRadius: { xl: '0.75rem', '2xl': '1rem' },
      keyframes: {
        'fade-in': { from: { opacity: '0', transform: 'translateY(4px)' }, to: { opacity: '1', transform: 'none' } },
        // An order landing on the chart: the tag arrives from the entry point.
        'order-in': {
          from: { opacity: '0', transform: 'scale(0.82)' },
          to: { opacity: '1', transform: 'scale(1)' },
        },
        // Settled orders leave rather than vanish, so the result is readable.
        'order-out': { from: { opacity: '1' }, to: { opacity: '0' } },
        'order-pulse': { '0%,100%': { opacity: '0.55' }, '50%': { opacity: '1' } },
        // The direction glyph arrives, then stays put for as long as the
        // preview does. It settles rather than pulsing: it marks a level the
        // trader is reading, and something that keeps moving there is harder
        // to read past, not easier to see.
        'arrow-in': {
          '0%': { opacity: '0', transform: 'scale(0.72)' },
          '100%': { opacity: '0.95', transform: 'scale(1)' },
        },
        'pulse-up': { '0%,100%': { backgroundColor: 'transparent' }, '50%': { backgroundColor: 'rgb(var(--up) / 0.16)' } },
        'pulse-down': { '0%,100%': { backgroundColor: 'transparent' }, '50%': { backgroundColor: 'rgb(var(--down) / 0.16)' } },
        shimmer: { '100%': { transform: 'translateX(100%)' } },

        /*
         * The landing page.
         *
         * `rise` is the one every section uses: content starts a little low
         * and slightly transparent, and an observer adds the class as the
         * section comes into view. `float` and `drift` move the artwork, slow
         * enough that the eye reads them as depth rather than motion.
         */
        'panel-in': {
          from: { opacity: '0', transform: 'translateX(24px)' },
          to: { opacity: '1', transform: 'translateX(0)' },
        },
        rise: {
          from: { opacity: '0', transform: 'translateY(22px)' },
          to: { opacity: '1', transform: 'translateY(0)' },
        },
        float: {
          '0%,100%': { transform: 'translateY(0)' },
          '50%': { transform: 'translateY(-14px)' },
        },
        drift: {
          '0%,100%': { transform: 'translate3d(0,0,0) scale(1)' },
          '50%': { transform: 'translate3d(18px,-24px,0) scale(1.06)' },
        },
        'candle-grow': {
          from: { transform: 'scaleY(0)' },
          to: { transform: 'scaleY(1)' },
        },
        marquee: {
          from: { transform: 'translateX(0)' },
          to: { transform: 'translateX(-50%)' },
        },
      },
      animation: {
        'fade-in': 'fade-in 180ms ease-out',
        'panel-in': 'panel-in 260ms cubic-bezier(0.22, 0.9, 0.3, 1) both',
        rise: 'rise 620ms cubic-bezier(0.22, 0.9, 0.3, 1) both',
        float: 'float 7s ease-in-out infinite',
        drift: 'drift 18s ease-in-out infinite',
        'candle-grow': 'candle-grow 620ms cubic-bezier(0.22, 0.9, 0.3, 1) both',
        marquee: 'marquee 28s linear infinite',
        'order-in': 'order-in 200ms cubic-bezier(0.2, 0.9, 0.3, 1.2)',
        'order-out': 'order-out 500ms ease-in forwards',
        'order-pulse': 'order-pulse 1.4s ease-in-out infinite',
        'arrow-in': 'arrow-in 220ms cubic-bezier(0.2, 0.9, 0.3, 1.2) forwards',
        'pulse-up': 'pulse-up 600ms ease-out',
        'pulse-down': 'pulse-down 600ms ease-out',
      },
    },
  },
  plugins: [],
};
