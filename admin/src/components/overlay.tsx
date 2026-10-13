import { useEffect, useId, useRef, useState, type ReactNode } from 'react';
import clsx from 'clsx';
import { X } from 'lucide-react';
import { Spinner } from './ui';

/**
 * Drawer and confirmation dialog.
 *
 * Overlays stack (a confirmation opens on top of a detail drawer), so Escape
 * closes only the topmost one, and focus returns to whatever opened it.
 */
const overlayStack: number[] = [];
let overlaySeq = 0;

function useOverlay(onClose: () => void, initialFocus: React.RefObject<HTMLElement>) {
  const closeRef = useRef(onClose);
  closeRef.current = onClose;

  useEffect(() => {
    const token = ++overlaySeq;
    overlayStack.push(token);
    const opener = document.activeElement as HTMLElement | null;
    initialFocus.current?.focus();

    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && overlayStack[overlayStack.length - 1] === token) {
        event.preventDefault();
        closeRef.current();
      }
    };
    window.addEventListener('keydown', onKey);
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';

    return () => {
      window.removeEventListener('keydown', onKey);
      overlayStack.splice(overlayStack.indexOf(token), 1);
      if (overlayStack.length === 0) document.body.style.overflow = previousOverflow;
      opener?.focus?.();
    };
    // Mount/unmount only: the latest onClose is read through the ref.
  }, []);
}

export function Drawer({
  title,
  subtitle,
  onClose,
  children,
  footer,
}: {
  title: ReactNode;
  subtitle?: ReactNode;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
}) {
  const closeButton = useRef<HTMLButtonElement>(null);
  const titleId = useId();
  useOverlay(onClose, closeButton);

  return (
    <div className="fixed inset-0 z-50 flex justify-end" role="dialog" aria-modal="true" aria-labelledby={titleId}>
      {/* Mouse convenience only; keyboard users have Escape and the close button. */}
      <div className="absolute inset-0 bg-scrim/70 backdrop-blur-sm" onClick={onClose} aria-hidden="true" />
      <div
        className={clsx(
          'relative flex h-full w-full max-w-3xl flex-col border-l border-surface-700 bg-surface-900',
          'shadow-overlay animate-fade-in',
        )}
      >
        <div className="flex items-start gap-3 border-b border-surface-800 px-4 py-3">
          <div className="min-w-0">
            <h2 id={titleId} className="truncate text-sm font-semibold text-content-100">
              {title}
            </h2>
            {subtitle ? <div className="mt-0.5 text-[11px] text-content-400">{subtitle}</div> : null}
          </div>
          <button
            ref={closeButton}
            className="ml-auto rounded-lg p-1.5 text-content-400 hover:bg-surface-800 hover:text-content-100"
            onClick={onClose}
            aria-label="Close"
          >
            <X className="h-4 w-4" />
          </button>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto">{children}</div>
        {footer ? (
          <div className="flex flex-wrap items-center justify-end gap-2 border-t border-surface-800 px-4 py-3">{footer}</div>
        ) : null}
      </div>
    </div>
  );
}

export interface NoteRule {
  label: string;
  required: boolean;
  placeholder?: string;
  hint?: string;
  min?: number;
  max?: number;
}

/** Note validation shared by every audited action: 3-500 characters when required. */
export function noteError(note: string, rule: NoteRule): string | null {
  const min = rule.min ?? 3;
  const max = rule.max ?? 500;
  const length = note.trim().length;
  if (length === 0) return rule.required ? 'A note is required' : null;
  if (length < min) return 'At least ' + min + ' characters';
  if (length > max) return 'At most ' + max + ' characters';
  return null;
}

export function ConfirmDialog({
  title,
  children,
  confirmLabel,
  tone = 'primary',
  note,
  pending,
  error,
  onConfirm,
  onCancel,
}: {
  title: string;
  children: ReactNode;
  confirmLabel: string;
  tone?: 'primary' | 'danger';
  note?: NoteRule;
  pending?: boolean;
  /** The server's reason the last attempt failed, shown in place. */
  error?: string | null;
  onConfirm: (note: string) => void;
  onCancel: () => void;
}) {
  const [text, setText] = useState('');
  const [touched, setTouched] = useState(false);
  const noteRef = useRef<HTMLTextAreaElement>(null);
  const cancelRef = useRef<HTMLButtonElement>(null);
  const titleId = useId();
  const noteId = useId();
  useOverlay(() => {
    if (!pending) onCancel();
  }, (note ? noteRef : cancelRef) as React.RefObject<HTMLElement>);

  const problem = note ? noteError(text, note) : null;
  const max = note?.max ?? 500;

  return (
    <div className="fixed inset-0 z-[60] grid place-items-center p-4" role="alertdialog" aria-modal="true" aria-labelledby={titleId}>
      <div
        className="absolute inset-0 bg-scrim/75 backdrop-blur-sm"
        onClick={() => (pending ? undefined : onCancel())}
        aria-hidden="true"
      />
      <form
        className="panel relative w-full max-w-md p-5 shadow-overlay animate-fade-in"
        onSubmit={(event) => {
          event.preventDefault();
          setTouched(true);
          if (problem || pending) return;
          onConfirm(text.trim());
        }}
      >
        <h2 id={titleId} className="text-sm font-semibold text-content-100">
          {title}
        </h2>
        <div className="mt-2 space-y-2 text-xs text-content-300">{children}</div>

        {note ? (
          <div className="mt-4">
            <label className="label" htmlFor={noteId}>
              {note.label}
              {note.required ? '' : ' (optional)'}
            </label>
            <textarea
              id={noteId}
              ref={noteRef}
              className="field min-h-[84px] resize-y"
              placeholder={note.placeholder}
              value={text}
              maxLength={max + 50}
              aria-invalid={touched && !!problem}
              aria-describedby={noteId + '-hint'}
              onChange={(event) => setText(event.target.value)}
              // Only once something was typed: focus moving on open must not flag an empty field.
              onBlur={() => {
                if (text.length > 0) setTouched(true);
              }}
            />
            <div id={noteId + '-hint'} className="mt-1 flex gap-2 text-[11px]">
              <span className={clsx(touched && problem ? 'text-down' : 'text-content-400')}>
                {touched && problem ? problem : note.hint ?? (note.required ? 'Required, 3-' + max + ' characters' : '')}
              </span>
              <span className={clsx('ml-auto tnum', text.trim().length > max ? 'text-down' : 'text-content-400')}>
                {text.trim().length} / {max}
              </span>
            </div>
          </div>
        ) : null}

        {error ? (
          <p className="mt-3 rounded-lg border border-down/40 bg-down/10 px-3 py-2 text-xs text-down" role="alert">
            {error}
          </p>
        ) : null}

        <div className="mt-5 flex justify-end gap-2">
          <button ref={cancelRef} type="button" className="btn-ghost" onClick={onCancel} disabled={pending}>
            Cancel
          </button>
          <button type="submit" className={tone === 'danger' ? 'btn-danger' : 'btn-primary'} disabled={pending}>
            {pending ? <Spinner className="h-3.5 w-3.5" /> : null}
            {confirmLabel}
          </button>
        </div>
      </form>
    </div>
  );
}
