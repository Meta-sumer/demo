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
        up: { DEFAULT: token('up'), soft: token('up-soft'), solid: token('up-solid') },
        down: { DEFAULT: token('down'), soft: token('down-soft'), solid: token('down-solid') },
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
        'pulse-up': { '0%,100%': { backgroundColor: 'transparent' }, '50%': { backgroundColor: 'rgb(var(--up) / 0.16)' } },
        'pulse-down': { '0%,100%': { backgroundColor: 'transparent' }, '50%': { backgroundColor: 'rgb(var(--down) / 0.16)' } },
        shimmer: { '100%': { transform: 'translateX(100%)' } },
      },
      animation: {
        'fade-in': 'fade-in 180ms ease-out',
        'pulse-up': 'pulse-up 600ms ease-out',
        'pulse-down': 'pulse-down 600ms ease-out',
      },
    },
  },
  plugins: [],
};
