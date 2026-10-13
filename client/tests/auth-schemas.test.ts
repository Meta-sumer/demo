import { describe, expect, it } from 'vitest';
import { firstIssue, loginSchema, registerSchema } from '../src/features/auth/schemas';

/**
 * The sign-in rules.
 *
 * Two screens submit these credentials now - the standalone /login and
 * /register pages, and the panel that slides out of the landing page - so the
 * rules live in one module. That only helps if the rules themselves are
 * pinned: a loosened password check would otherwise be caught by nobody until
 * the server rejected a user who had already filled the form in.
 */
describe('login', () => {
  it('accepts an email and a password', () => {
    expect(loginSchema.safeParse({ email: 'a@b.co', password: 'x' }).success).toBe(true);
  });

  it.each([
    ['not an email', { email: 'nope', password: 'x' }, 'valid email'],
    ['an empty password', { email: 'a@b.co', password: '' }, 'Enter your password'],
  ])('rejects %s', (_label, input, message) => {
    const result = loginSchema.safeParse(input);
    expect(result.success).toBe(false);
    if (!result.success) expect(firstIssue(result.error)).toContain(message);
  });
});

describe('registration', () => {
  const valid = {
    email: 'trader@example.com',
    username: 'new_trader1',
    fullName: 'New Trader',
    password: 'Passw0rdy',
  };

  it('accepts a complete form', () => {
    expect(registerSchema.safeParse(valid).success).toBe(true);
  });

  /*
   * The password rules are the ones worth spelling out. Each of these is a
   * separate refinement, and a regex edited in isolation can silently drop
   * one of them while the others still pass.
   */
  it.each([
    ['too short', 'Pw0rdy', 'at least 8 characters'],
    ['no uppercase', 'passw0rdy', 'uppercase'],
    ['no lowercase', 'PASSW0RDY', 'lowercase'],
    ['no digit', 'Passwordy', 'digit'],
  ])('rejects a password that is %s', (_label, password, message) => {
    const result = registerSchema.safeParse({ ...valid, password });
    expect(result.success).toBe(false);
    if (!result.success) expect(firstIssue(result.error)).toContain(message);
  });

  it.each([
    ['too short', 'ab'],
    ['too long', 'a'.repeat(25)],
    ['punctuated', 'not-allowed'],
    ['spaced', 'not allowed'],
  ])('rejects a username that is %s', (_label, username) => {
    expect(registerSchema.safeParse({ ...valid, username }).success).toBe(false);
  });

  it('allows letters, digits and underscores in a username', () => {
    expect(registerSchema.safeParse({ ...valid, username: 'a_1B' }).success).toBe(true);
  });

  it('needs a name of at least two characters', () => {
    expect(registerSchema.safeParse({ ...valid, fullName: 'A' }).success).toBe(false);
    expect(registerSchema.safeParse({ ...valid, fullName: 'Al' }).success).toBe(true);
  });
});

describe('firstIssue', () => {
  it('reports the first problem rather than all of them', () => {
    const result = registerSchema.safeParse({ email: 'no', username: '', fullName: '', password: '' });
    expect(result.success).toBe(false);
    if (!result.success) {
      expect(result.error.issues.length).toBeGreaterThan(1);
      expect(firstIssue(result.error)).toBe('Enter a valid email address');
    }
  });
});
