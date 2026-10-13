import { File, Paths } from 'expo-file-system';
import * as Sharing from 'expo-sharing';

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
 * Writes the file to the app's cache and opens the share sheet, so it can be
 * saved to Files, mailed or opened in a spreadsheet app.
 *
 * A BOM is prepended so Excel reads it as UTF-8; without it, any non-ASCII
 * character - a rupee sign, an accented name - is mangled on open.
 */
export async function shareCsv(filename: string, csv: string): Promise<void> {
  const file = new File(Paths.cache, filename);
  if (file.exists) file.delete();
  file.create();
  file.write('﻿' + csv);
  if (await Sharing.isAvailableAsync()) {
    await Sharing.shareAsync(file.uri, { mimeType: 'text/csv', dialogTitle: filename, UTI: 'public.comma-separated-values-text' });
  }
}
