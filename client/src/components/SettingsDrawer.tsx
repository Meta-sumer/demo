import clsx from 'clsx';
import { Check, Globe, Minus, Moon, Monitor, Plus, RotateCcw, Sun, SunMoon, X } from 'lucide-react';
import {
  GRID_OPACITY_MAX,
  timeframeLabel,
  TIMEFRAMES,
  useSettingsStore,
  type ChartType,
} from '../stores/settingsStore';
import { useThemeStore, type ThemePreference } from '../stores/themeStore';
import { useWorkspaceStore } from '../stores/workspaceStore';
import { usePanelTransition } from '../hooks/usePanelTransition';
import { useChartPalette } from '../utils/theme-colors';
import { TIME_ZONES, zoneLabel } from '../utils/time';
import { formatExpiry, formatMoney, fromMinor, toMinor } from '../utils/format';
import { OneTapSwitch, TradeModeControl } from './TradingPreferences';

/**
 * Settings panel.
 *
 * Opening and closing are the same control in the rail, so the panel has to
 * read as one object moving rather than appearing: on a wide screen it opens
 * its width, and below lg it slides in from the edge.
 *
 * On a wide screen it sits beside the icon rail and takes width from the
 * workspace rather than covering it - most of these are adjustments a trader
 * makes *while watching a position*, so the chart has to stay visible and
 * usable with the panel open. Below `lg` there is no width to give, so it
 * falls back to an overlay.
 */
/** Long enough to read as a movement, short enough not to be in the way. */
const TRANSITION_MS = 320;
const EASE = 'ease-[cubic-bezier(0.32,0.72,0,1)]';

const TEMPLATES: Array<{
  value: ThemePreference;
  label: string;
  icon: typeof Sun;
}> = [
  { value: 'light', label: 'Light Mode', icon: Sun },
  { value: 'twilight', label: 'Twilight', icon: SunMoon },
  { value: 'dark', label: 'Full Night', icon: Moon },
  { value: 'system', label: 'System', icon: Monitor },
];

const CHART_TYPES: Array<{ value: ChartType; label: string }> = [
  { value: 'candles', label: 'Candles' },
  { value: 'bars', label: 'Bars' },
  { value: 'line', label: 'Line' },
  { value: 'area', label: 'Area' },
];

export function SettingsDrawer() {
  const open = useWorkspaceStore((s) => s.settingsOpen);
  const setOpen = useWorkspaceStore((s) => s.setSettingsOpen);
  const preference = useThemeStore((s) => s.preference);
  const resolved = useThemeStore((s) => s.resolved);
  const setPreference = useThemeStore((s) => s.setPreference);
  const settings = useSettingsStore();
  const palette = useChartPalette();
  const { mounted, entered } = usePanelTransition(open, TRANSITION_MS);

  if (!mounted) return null;

  return (
    <>
      {/* Only below lg, where the panel has to cover the workspace. */}
      <button
        className={clsx(
          'fixed inset-0 z-40 bg-scrim/60 backdrop-blur-sm transition-opacity lg:hidden',
          EASE,
          entered ? 'opacity-100' : 'pointer-events-none opacity-0',
        )}
        style={{ transitionDuration: TRANSITION_MS + 'ms' }}
        onClick={() => setOpen(false)}
        aria-label="Close settings"
      />

      <aside
        className={clsx(
          'flex overflow-hidden border-surface-700 bg-surface-900',
          'fixed inset-y-0 left-0 z-50 w-[min(340px,90vw)] border-r shadow-overlay',
          'lg:relative lg:inset-auto lg:z-auto lg:shrink-0 lg:shadow-none',
          'transition-[width,transform]',
          EASE,
          entered
            ? 'max-lg:translate-x-0 lg:w-[330px] lg:border-r'
            : 'max-lg:-translate-x-full lg:w-0 lg:border-r-0',
        )}
        style={{ transitionDuration: TRANSITION_MS + 'ms' }}
        role="dialog"
        aria-label="Settings"
        aria-hidden={!entered}
      >
        {/* Fixed width so the content does not reflow while the shell animates
            its width open - text rewrapping mid-transition reads as a glitch. */}
        <div className="flex h-full w-[min(340px,90vw)] shrink-0 flex-col lg:w-[330px]">
          <div className="flex shrink-0 items-center gap-3 border-b border-surface-800 px-4 py-3">
            <h2 className="text-lg font-bold text-content-100">Settings</h2>
            <button
              onClick={() => setOpen(false)}
              className="ml-auto rounded-lg p-1.5 text-content-400 transition hover:bg-surface-850 hover:text-content-200"
              aria-label="Close settings"
            >
              <X className="h-4 w-4" />
            </button>
          </div>

          <div className="min-h-0 flex-1 space-y-5 overflow-y-auto p-4">
            <Field label="Language" htmlFor="settingsLanguage">
              <div className="relative">
                <Globe className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-content-400" />
                <select id="settingsLanguage" className="field pl-9" defaultValue="en" disabled>
                  <option value="en">English</option>
                </select>
              </div>
              <Hint>English is the only bundled locale - no other translation ships yet.</Hint>
            </Field>

            <Field label="Timezone" htmlFor="settingsTimezone">
              <select
                id="settingsTimezone"
                className="field"
                value={settings.timezone}
                onChange={(event) => settings.setTimezone(event.target.value)}
              >
                {TIME_ZONES.map((zone) => (
                  <option key={zone} value={zone}>
                    {zoneLabel(zone)}
                  </option>
                ))}
              </select>
              <Hint>Changes how times are written. Expiries are always decided in UTC on the server.</Hint>
            </Field>

            {/* The same two controls as Account → Preferences and the
                Settings page, so their names and meaning never drift. */}
            <section>
              <SectionTitle>Trading</SectionTitle>
              <div className="space-y-3">
                <TradeModeControl />
                <OneTapSwitch />
              </div>
            </section>

            <section>
              <SectionTitle>Template</SectionTitle>
              <div className="space-y-2" role="radiogroup" aria-label="Colour theme">
                {TEMPLATES.map((template) => {
                  const active = preference === template.value;
                  return (
                    <button
                      key={template.value}
                      type="button"
                      role="radio"
                      aria-checked={active}
                      onClick={() => setPreference(template.value)}
                      className={clsx(
                        'flex w-full items-center gap-3 rounded-full border px-4 py-3 text-left transition',
                        active
                          ? 'border-accent bg-surface-850 shadow-panel'
                          : 'border-surface-700 bg-surface-850/60 hover:border-surface-600',
                      )}
                    >
                      <template.icon
                        className={clsx('h-4 w-4 shrink-0', active ? 'text-accent' : 'text-content-400')}
                      />
                      <span className="min-w-0 flex-1 truncate text-sm font-semibold text-content-100">
                        {template.label}
                        {template.value === 'system' && active ? (
                          <span className="ml-1.5 font-normal text-content-400">({resolved})</span>
                        ) : null}
                      </span>
                      <span
                        className={clsx(
                          'h-4 w-4 shrink-0 rounded-full border-2 transition',
                          active ? 'border-accent bg-accent' : 'border-surface-600',
                        )}
                      />
                    </button>
                  );
                })}
              </div>
            </section>

            <section>
              <div className="mb-2 flex items-center justify-between">
                <SectionTitle className="mb-0">Platform</SectionTitle>
                <button
                  onClick={settings.resetChartAppearance}
                  className="inline-flex items-center gap-1 text-[10px] font-semibold text-content-400 transition hover:text-content-200"
                >
                  <RotateCcw className="h-3 w-3" />
                  Reset
                </button>
              </div>

              <div className="space-y-3">
                <Field label="Grid's opacity" htmlFor="settingsGrid">
                  <Stepper
                    id="settingsGrid"
                    value={settings.gridOpacity}
                    min={0}
                    max={GRID_OPACITY_MAX}
                    onChange={settings.setGridOpacity}
                  />
                </Field>

                <CheckRow
                  label="Auto-scrolling"
                  description="Automatic graphic scrolling"
                  checked={settings.autoScroll}
                  onChange={settings.toggleAutoScroll}
                />
                <CheckRow
                  label="Performance Mode"
                  description="Use optimized rendering for chart and candles"
                  checked={settings.performanceMode}
                  onChange={settings.togglePerformanceMode}
                />
                <CheckRow
                  label="Sound on settlement"
                  description="A short cue when a trade settles"
                  checked={settings.soundEnabled}
                  onChange={settings.toggleSound}
                />
                <CheckRow
                  label="Short order label"
                  description="Direction and stake beside each entry line"
                  checked={settings.showOrderLabels}
                  onChange={settings.toggleOrderLabels}
                />
                <CheckRow
                  label="Hide trade results"
                  description="Clear entry lines, arrows and boundaries off the chart"
                  checked={settings.hideTradeResults}
                  onChange={settings.toggleHideTradeResults}
                />
              </div>
            </section>

            <section>
              <SectionTitle>Graph</SectionTitle>
              <div className="space-y-3">
                <Field label="Chart type">
                  <div className="grid grid-cols-4 gap-1">
                    {CHART_TYPES.map((type) => (
                      <button
                        key={type.value}
                        onClick={() => settings.setChartType(type.value)}
                        className={clsx(
                          'rounded-lg px-1 py-2 text-[11px] font-semibold transition',
                          settings.chartType === type.value
                            ? 'bg-accent text-surface-950'
                            : 'bg-surface-850 text-content-400 hover:text-content-200',
                        )}
                      >
                        {type.label}
                      </button>
                    ))}
                  </div>
                </Field>

                <Field label="Timeframe">
                  <div className="grid grid-cols-5 gap-1">
                    {TIMEFRAMES.map((seconds) => (
                      <button
                        key={seconds}
                        onClick={() => settings.setTimeframe(seconds)}
                        className={clsx(
                          'tnum rounded-lg px-1 py-2 text-[11px] font-semibold transition',
                          settings.timeframe === seconds
                            ? 'bg-accent text-surface-950'
                            : 'bg-surface-850 text-content-400 hover:text-content-200',
                        )}
                      >
                        {timeframeLabel(seconds)}
                      </button>
                    ))}
                  </div>
                </Field>

                <Field label="Graph colours">
                  <div className="flex gap-2">
                    <ColourField
                      label="Up"
                      value={settings.upColorOverride ?? palette.up}
                      overridden={settings.upColorOverride !== null}
                      onChange={settings.setUpColor}
                    />
                    <ColourField
                      label="Down"
                      value={settings.downColorOverride ?? palette.down}
                      overridden={settings.downColorOverride !== null}
                      onChange={settings.setDownColor}
                    />
                  </div>
                </Field>
              </div>
            </section>

            <section>
              <SectionTitle>Defaults</SectionTitle>
              <div className="space-y-3">
                <Field label="Default investment" htmlFor="drawerStake">
                  <input
                    id="drawerStake"
                    inputMode="decimal"
                    className="field tnum"
                    value={fromMinor(settings.defaultStake)}
                    onChange={(event) => {
                      const parsed = Number(event.target.value.replace(/[^0-9.]/g, ''));
                      if (Number.isFinite(parsed)) settings.setDefaultStake(toMinor(parsed));
                    }}
                  />
                  <Hint>Pre-filled on the ticket: {formatMoney(settings.defaultStake)}</Hint>
                </Field>

                <Field label="Default expiry">
                  <div className="flex gap-1">
                    {[30, 60, 300].map((seconds) => (
                      <button
                        key={seconds}
                        onClick={() => settings.setDefaultExpiry(seconds)}
                        className={clsx(
                          'flex-1 rounded-lg px-2 py-2 text-xs font-semibold transition',
                          settings.defaultExpirySeconds === seconds
                            ? 'bg-accent text-surface-950'
                            : 'bg-surface-850 text-content-400 hover:text-content-200',
                        )}
                      >
                        {formatExpiry(seconds)}
                      </button>
                    ))}
                  </div>
                </Field>
              </div>
            </section>
          </div>
        </div>
      </aside>
    </>
  );
}

/**
 * Labelled control.
 *
 * The label sits on the field's top border the way the reference console does,
 * which keeps the name readable once a value is filled in.
 */
function Field({ label, htmlFor, children }: { label: string; htmlFor?: string; children: React.ReactNode }) {
  return (
    <div className="rounded-xl border border-surface-700 bg-surface-850/60 px-3 pb-3 pt-2">
      <label htmlFor={htmlFor} className="mb-1.5 block text-[11px] font-medium text-content-400">
        {label}
      </label>
      {children}
    </div>
  );
}

function Hint({ children }: { children: React.ReactNode }) {
  return <p className="mt-1.5 text-[10px] leading-snug text-content-400">{children}</p>;
}

function SectionTitle({ children, className }: { children: React.ReactNode; className?: string }) {
  return (
    <h3 className={clsx('mb-2 text-[10px] font-bold uppercase tracking-widest text-content-400', className)}>
      {children}
    </h3>
  );
}

/** Numeric dial with the decrement and increment buttons either side. */
function Stepper({
  id,
  value,
  min,
  max,
  onChange,
}: {
  id: string;
  value: number;
  min: number;
  max: number;
  onChange: (value: number) => void;
}) {
  return (
    <div className="flex items-center gap-2">
      <button
        type="button"
        onClick={() => onChange(value - 1)}
        disabled={value <= min}
        aria-label="Decrease"
        className="grid h-8 w-8 shrink-0 place-items-center rounded-lg bg-surface-800 text-content-300 transition enabled:hover:text-content-100 disabled:opacity-40"
      >
        <Minus className="h-3.5 w-3.5" />
      </button>
      <output id={id} className="tnum flex-1 text-center text-base font-bold text-content-100">
        {value}
      </output>
      <button
        type="button"
        onClick={() => onChange(value + 1)}
        disabled={value >= max}
        aria-label="Increase"
        className="grid h-8 w-8 shrink-0 place-items-center rounded-lg bg-surface-800 text-content-300 transition enabled:hover:text-content-100 disabled:opacity-40"
      >
        <Plus className="h-3.5 w-3.5" />
      </button>
    </div>
  );
}

/** Checkbox row: a square control, a label, and the reason it exists. */
function CheckRow({
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
      role="checkbox"
      aria-checked={checked}
      className="flex w-full items-start gap-3 rounded-xl px-1 py-1.5 text-left transition hover:bg-surface-850/60"
    >
      <span
        className={clsx(
          'mt-0.5 grid h-4 w-4 shrink-0 place-items-center rounded border-2 transition',
          checked ? 'border-accent bg-accent' : 'border-surface-600',
        )}
      >
        {checked ? <Check className="h-3 w-3 text-white" strokeWidth={3} /> : null}
      </span>
      <span className="min-w-0 flex-1">
        <span className="block text-sm font-semibold text-content-100">{label}</span>
        <span className="block text-[11px] leading-snug text-content-400">{description}</span>
      </span>
    </button>
  );
}

/**
 * Colour override.
 *
 * Defaults to following the theme; the swatch only becomes an override once the
 * trader picks something, so switching template still repaints the chart for
 * anyone who never touched this.
 */
function ColourField({
  label,
  value,
  overridden,
  onChange,
}: {
  label: string;
  value: string;
  overridden: boolean;
  onChange: (hex: string | null) => void;
}) {
  return (
    <div className="min-w-0 flex-1">
      <span className="mb-1 block text-[10px] font-medium text-content-400">{label}</span>
      <div className="flex items-center gap-1.5">
        <input
          type="color"
          value={value}
          onChange={(event) => onChange(event.target.value)}
          aria-label={label + ' colour'}
          className="h-7 w-9 shrink-0 cursor-pointer rounded-md border border-surface-700 bg-surface-900 p-0.5"
        />
        <button
          type="button"
          onClick={() => onChange(null)}
          disabled={!overridden}
          className="min-w-0 flex-1 truncate rounded-md px-1 text-left text-[10px] font-medium text-content-400 transition enabled:hover:text-content-200 disabled:opacity-50"
        >
          {overridden ? 'Use theme' : 'Theme'}
        </button>
      </div>
    </div>
  );
}
