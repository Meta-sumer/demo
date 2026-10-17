import { useState } from 'react';
import { CopyButton } from './usdt';
import type { MfaEnrolment } from '../types/api';

/** Keeps what an authenticator code can contain: digits, at most six. */
export function cleanCode(value: string): string {
  return value.replace(/\D/g, '').slice(0, 6);
}

/** The 6-digit authenticator code field. */
export function CodeInput({
  id,
  value,
  onChange,
  autoFocus,
}: {
  id: string;
  value: string;
  onChange: (value: string) => void;
  autoFocus?: boolean;
}) {
  return (
    <input
      id={id}
      className="field text-center font-mono text-lg tracking-[0.4em]"
      inputMode="numeric"
      autoComplete="one-time-code"
      autoFocus={autoFocus}
      maxLength={6}
      placeholder="000000"
      value={value}
      onChange={(event) => onChange(cleanCode(event.target.value))}
    />
  );
}

/**
 * How to add the account to an authenticator app. There is no QR code: the
 * operator types the key (or pastes the otpauth link) into the app.
 */
export function EnrolmentSetup({ enrolment }: { enrolment: MfaEnrolment }) {
  return (
    <div className="space-y-3 text-sm">
      <ol className="list-decimal space-y-1 pl-5 text-content-300">
        <li>Open an authenticator app (Google Authenticator, Microsoft Authenticator, Authy, 1Password).</li>
        <li>Add an account, choose "Enter a setup key", and type the key below.</li>
        <li>Enter the 6-digit code the app shows.</li>
      </ol>

      <div className="rounded-lg border border-surface-700 bg-surface-900 p-3">
        <div className="text-[11px] text-content-400">Setup key ({enrolment.account})</div>
        <div className="mt-1 flex items-center gap-2">
          <code className="break-all font-mono text-sm text-content-100">{enrolment.secretGrouped}</code>
          <CopyButton value={enrolment.secret} label="setup key" />
        </div>
        <div className="mt-2 text-[11px] text-content-400">
          Time based, {enrolment.digits} digits, every {enrolment.period} seconds ({enrolment.algorithm})
        </div>
      </div>

      <details className="text-[11px] text-content-400">
        <summary className="cursor-pointer">Use a setup link instead</summary>
        <div className="mt-1 flex items-start gap-2">
          <code className="break-all font-mono text-content-300">{enrolment.otpauthUri}</code>
          <CopyButton value={enrolment.otpauthUri} label="setup link" />
        </div>
      </details>
    </div>
  );
}

/**
 * One-time recovery codes. They are shown only once, so the operator has to
 * confirm they saved them before moving on.
 */
export function RecoveryCodes({
  codes,
  continueLabel,
  onContinue,
}: {
  codes: string[];
  continueLabel: string;
  onContinue: () => void;
}) {
  const [saved, setSaved] = useState(false);
  const text = codes.join('\n');

  function download() {
    const blob = new Blob([text + '\n'], { type: 'text/plain' });
    const url = URL.createObjectURL(blob);
    const link = document.createElement('a');
    link.href = url;
    link.download = 'admin-recovery-codes.txt';
    link.click();
    URL.revokeObjectURL(url);
  }

  return (
    <div className="space-y-3 text-sm">
      <p className="text-content-300">
        Keep these recovery codes somewhere safe. Each one works once, if you lose your phone. They will not be
        shown again.
      </p>
      <div className="grid grid-cols-2 gap-2 rounded-lg border border-surface-700 bg-surface-900 p-3">
        {codes.map((code) => (
          <code key={code} className="font-mono text-sm text-content-100">
            {code}
          </code>
        ))}
      </div>
      <div className="flex items-center gap-2 text-[11px] text-content-400">
        <CopyButton value={text} label="recovery codes" />
        <span>Copy</span>
        <button type="button" className="ml-3 underline hover:text-content-100" onClick={download}>
          Download as .txt
        </button>
      </div>
      <label className="flex items-center gap-2 text-content-200">
        <input type="checkbox" checked={saved} onChange={(event) => setSaved(event.target.checked)} />
        I have saved these codes
      </label>
      <button type="button" className="btn-primary w-full" disabled={!saved} onClick={onContinue}>
        {continueLabel}
      </button>
    </div>
  );
}
