import { clsx, type ClassValue } from 'clsx';
import { twMerge } from 'tailwind-merge';

/**
 * Joins class names, later ones winning a conflict.
 *
 * NativeWind resolves two classes for the same property by stylesheet order,
 * not by where they sit in the string, so `cn('text-content-100',
 * 'text-down')` has to drop the first rather than leave it to chance. That is
 * what lets components carry defaults a caller can override.
 */
export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs));
}
