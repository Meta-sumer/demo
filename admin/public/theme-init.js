/*
 * Applies the stored theme before first paint.
 *
 * Without this the page renders with the default theme and then swaps,
 * which is a visible white flash for a dark-mode user on every load.
 * Loaded as a plain (blocking) script from index.html and kept
 * dependency-free for that reason. It is a file rather than an inline
 * script so the Content-Security-Policy can stay script-src 'self'
 * (deploy/vps/nginx/wintratrade-security-headers.conf). The storage key
 * must match THEME_STORAGE_KEY in src/stores/themeStore.ts.
 */
(function () {
  try {
    var stored = localStorage.getItem('bt.theme');
    var resolved =
      stored === 'light' || stored === 'dark' || stored === 'twilight'
        ? stored
        : window.matchMedia('(prefers-color-scheme: dark)').matches
          ? 'dark'
          : 'light';
    document.documentElement.dataset.theme = resolved;
    document.documentElement.style.colorScheme = resolved;
  } catch (e) {
    document.documentElement.dataset.theme = 'dark';
  }
})();
