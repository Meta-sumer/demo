import { z } from 'zod';

/**
 * The sign-in and sign-up rules, in one place.
 *
 * Both the standalone pages and the panel that slides out of the landing page
 * submit the same credentials to the same endpoints, so they have to agree on
 * what is valid. Kept here rather than in either screen so adding a third way
 * in cannot quietly introduce a fourth set of rules.
 *
 * These mirror the server's own validation. Duplicating it buys the user an
 * answer before the round trip; the server remains the one that decides.
 */
export const loginSchema = z.object({
  email: z.string().email('Enter a valid email address'),
  password: z.string().min(1, 'Enter your password'),
});

export const registerSchema = z.object({
  email: z.string().email('Enter a valid email address'),
  username: z
    .string()
    .min(3, 'Username must be at least 3 characters')
    .max(24)
    .regex(/^[a-zA-Z0-9_]+$/, 'Letters, digits and underscores only'),
  fullName: z.string().min(2, 'Enter your name').max(120),
  password: z
    .string()
    .min(8, 'Password must be at least 8 characters')
    .regex(/[a-z]/, 'Include a lowercase letter')
    .regex(/[A-Z]/, 'Include an uppercase letter')
    .regex(/[0-9]/, 'Include a digit'),
});

export type LoginValues = z.infer<typeof loginSchema>;
export type RegisterValues = z.infer<typeof registerSchema>;

/** The first message a failed parse should show, in field order. */
export function firstIssue(error: z.ZodError): string {
  return error.issues[0]?.message ?? 'Check the form';
}
