import { useMemo, useState } from 'react';
import qrcode from 'qrcode-generator';
import { CopyButton } from './usdt';
import type { MfaEnrolment } from '../types/api';

/**
 * The otpauth link as a QR code, drawn here in the browser. The link holds
 * the secret, so it is never sent to an outside QR service. Dark modules on
 * white with a quiet zone, whatever the theme, so every scanner reads it.
 */
export function OtpQrCode({ uri, size = 184 }: { uri: string; size?: number }) {
  const qr = useMemo(() => {
    const code = qrcode(0, 'M');
    code.addData(uri);
    code.make();
    return code;
  }, [uri]);
  const count = qr.getModuleCount();
  const quiet = 4;
  const cells: string[] = [];
  for (let row = 0; row < count; row++) {
    for (let col = 0; col < count; col++) {
      if (qr.isDark(row, col)) cells.push('M' + (col + quiet) + ' ' + (row + quiet) + 'h1v1h-1z');
    }
  }
  const box = count + quiet * 2;
  return (
    <svg
      role="img"
      aria-label="QR code to add this account to an authenticator app"
      width={size}
      height={size}
      viewBox={'0 0 ' + box + ' ' + box}
      shapeRendering="crispEdges"
      className="rounded-md"
    >
      <rect width={box} height={box} fill="#ffffff" />
      <path d={cells.join('')} fill="#000000" />
    </svg>
  );
}

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
 * How to add the account to an authenticator app: scan the QR code, or type
 * the key (or paste the otpauth link) when the camera is not an option.
 */
export function EnrolmentSetup({ enrolment }: { enrolment: MfaEnrolment }) {
  return (
    <div className="space-y-3 text-sm">
      <ol className="list-decimal space-y-1 pl-5 text-content-300">
        <li>Open an authenticator app (Google Authenticator, Microsoft Authenticator, Authy, 1Password).</li>
        <li>Add an account and scan the QR code. No camera? Choose "Enter a setup key" and type the key below.</li>
        <li>Enter the 6-digit code the app shows.</li>
      </ol>

      <div className="flex justify-center">
        <OtpQrCode uri={enrolment.otpauthUri} />
      </div>
      <p className="text-center text-[11px] text-content-400">
        Do not share a screenshot of this code or the key: they let anyone generate your sign-in codes.
      </p>

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
