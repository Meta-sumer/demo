/**
 * CSV export.
 *
 * Written by hand rather than pulled from a package, because the only hard part
 * is the quoting and getting that wrong is how an export silently corrupts:
 * a symbol containing a comma shifts every later column, and a value beginning
 * with `=` is executed as a formula when the file is opened in a spreadsheet.
 */

/** Characters that make a spreadsheet treat a cell as a formula, not text. */
const FORMULA_PREFIXES = ['=', '+', '-', '@', '\t', '\r'];

/**
 * Escapes one value for a CSV cell.
 *
 * Quotes are doubled and the field is wrapped whenever it contains a delimiter,
 * a quote or a newline. A leading formula character is neutralised with a
 * single quote so the value is read as text - an exported trade reference must
 * never be able to run as a spreadsheet formula.
 */
export function escapeCsvValue(value: unknown): string {
  if (value === null || value === undefined) return '';

  let text = String(value);

  if (FORMULA_PREFIXES.some((prefix) => text.startsWith(prefix))) {
    text = "'" + text;
  }

  if (/[",\n\r]/.test(text)) {
    return '"' + text.replace(/"/g, '""') + '"';
  }
  return text;
}

export interface CsvColumn<T> {
  header: string;
  value: (row: T) => unknown;
}

/**
 * Renders rows as a CSV document.
 *
 * CRLF line endings, which is what the format specifies and what Excel expects.
 */
export function toCsv<T>(rows: T[], columns: Array<CsvColumn<T>>): string {
  const lines: string[] = [columns.map((column) => escapeCsvValue(column.header)).join(',')];

  for (const row of rows) {
    lines.push(columns.map((column) => escapeCsvValue(column.value(row))).join(','));
  }

  return lines.join('\r\n');
}

/**
 * Hands the file to the browser.
 *
 * A BOM is prepended so Excel reads it as UTF-8; without it, any non-ASCII
 * character - a rupee sign, an accented name - is mangled on open.
 */
export function downloadCsv(filename: string, csv: string): void {
  const blob = new Blob(['﻿' + csv], { type: 'text/csv;charset=utf-8;' });
  const url = URL.createObjectURL(blob);

  const link = document.createElement('a');
  link.href = url;
  link.download = filename;
  document.body.appendChild(link);
  link.click();
  link.remove();

  // Released on the next tick; revoking synchronously can cancel the download.
  window.setTimeout(() => URL.revokeObjectURL(url), 1000);
}
