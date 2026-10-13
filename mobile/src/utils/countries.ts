/**
 * Countries offered in the profile.
 *
 * ISO 3166-1 alpha-2, which is what the server stores and validates (a
 * two-character code). Kept as a plain list rather than pulled from a package:
 * the set is small, and a dependency that ships every subdivision is far more
 * weight than this needs.
 */
export interface Country {
  code: string;
  name: string;
}

export const COUNTRIES: Country[] = [
  { code: 'IN', name: 'India' },
  { code: 'AE', name: 'United Arab Emirates' },
  { code: 'AU', name: 'Australia' },
  { code: 'BD', name: 'Bangladesh' },
  { code: 'BR', name: 'Brazil' },
  { code: 'CA', name: 'Canada' },
  { code: 'DE', name: 'Germany' },
  { code: 'EG', name: 'Egypt' },
  { code: 'ES', name: 'Spain' },
  { code: 'FR', name: 'France' },
  { code: 'GB', name: 'United Kingdom' },
  { code: 'ID', name: 'Indonesia' },
  { code: 'IT', name: 'Italy' },
  { code: 'JP', name: 'Japan' },
  { code: 'KE', name: 'Kenya' },
  { code: 'LK', name: 'Sri Lanka' },
  { code: 'MY', name: 'Malaysia' },
  { code: 'NG', name: 'Nigeria' },
  { code: 'NP', name: 'Nepal' },
  { code: 'NZ', name: 'New Zealand' },
  { code: 'PH', name: 'Philippines' },
  { code: 'PK', name: 'Pakistan' },
  { code: 'SA', name: 'Saudi Arabia' },
  { code: 'SG', name: 'Singapore' },
  { code: 'TH', name: 'Thailand' },
  { code: 'TR', name: 'Turkey' },
  { code: 'US', name: 'United States' },
  { code: 'VN', name: 'Vietnam' },
  { code: 'ZA', name: 'South Africa' },
];

/** Display name for a stored code, falling back to the code itself. */
export function countryName(code: string | null): string {
  if (!code) return 'Not set';
  return COUNTRIES.find((country) => country.code === code)?.name ?? code;
}
