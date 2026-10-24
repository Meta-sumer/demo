import { useEffect, useId, useRef, useState, type ReactNode } from 'react';
import clsx from 'clsx';
import type { LucideIcon } from 'lucide-react';
import { formatMs, formatRupees, fromMinor, rupeesInWords } from '../utils/format';
import { Badge } from './ui';

/**
 * The building blocks of the Settings page (pages/Settings.tsx and its
 * pricing-engine card, pages/PricingSettings.tsx).
 *
 * The form is uncontrolled: every field carries its published value as
 * data-published, so countChanges can tell how many differ without a second
 * list of the fields, and the submit handler reads FormData.
 */

/**
 * How many fields differ from the published version. Each field carries the
 * published value as data-published (React keeps a data attribute current,
 * where it never moves a select's defaultSelected), so this needs no second
 * list of the fields. The change note has none and is not counted.
 */
export function countChanges(form: HTMLFormElement): number {
  return Array.from(form.elements).filter(isChanged).length;
}

/**
 * The same count per collapsible group: fields inside an element with
 * data-section count towards that section, so a closed group can say it
 * holds unsaved changes.
 */
export function countChangesBySection(form: HTMLFormElement): Record<string, number> {
  const counts: Record<string, number> = {};
  for (const element of Array.from(form.elements)) {
    if (!isChanged(element)) continue;
    const section = element.closest<HTMLElement>('[data-section]')?.dataset.section;
    if (section) counts[section] = (counts[section] ?? 0) + 1;
  }
  return counts;
}

function isChanged(element: Element): boolean {
  if (!(element instanceof HTMLInputElement || element instanceof HTMLSelectElement)) return false;
  const published = element.dataset.published;
  if (published === undefined) return false;
  const current =
    element instanceof HTMLInputElement && element.type === 'checkbox'
      ? element.checked
        ? 'on'
        : 'off'
      : element.value;
  return current !== published;
}

/** One settings group: a card with an icon, a title and a one-line description. */
export function Section({
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
export function Note({ children }: { children: ReactNode }) {
  return <p className="col-span-full text-xs leading-relaxed text-content-400">{children}</p>;
}

/** The label row shared by every field, with an "Off" badge for a 0 = off setting at 0. */
export function FieldLabel({ htmlFor, label, off }: { htmlFor: string; label: string; off?: boolean }) {
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
export function FieldHelp({ children }: { children: ReactNode }) {
  return <p className="mt-1 min-h-[16px] text-[11px] leading-4 text-content-400">{children}</p>;
}

/**
 * Tracks what is typed in an uncontrolled input, for the help line. Re-reads
 * the input when the published value changes, because React then moves an
 * untouched field to the new value without an input event.
 */
export function useTyped(initial: string) {
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
export function MoneyField({
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
export function NumberField({
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
export function OptNumberField(props: Omit<Parameters<typeof NumberField>[0], 'value'> & { value: number | undefined }) {
  if (props.value === undefined) return null;
  return <NumberField {...props} value={props.value} />;
}

export function SelectField({
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

export function Toggle({
  name,
  label,
  hint,
  defaultChecked,
  disabled,
  danger,
}: {
  name: string;
  label: string;
  /** A second, quieter line: what the switch does. */
  hint?: ReactNode;
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
      {hint ? (
        <span className="min-w-0">
          <span className="block">{label}</span>
          <span className="mt-0.5 block text-[11px] font-normal leading-4 text-content-400">{hint}</span>
        </span>
      ) : (
        label
      )}
    </label>
  );
}
