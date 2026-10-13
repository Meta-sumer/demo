import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      // The dev server proxies the API so the browser sees a single origin and
      // no CORS preflight is needed while developing.
      '/api': { target: 'http://127.0.0.1:4000', changeOrigin: true, rewrite: (p) => p.replace(/^\/api/, '') },
      '/ws': { target: 'ws://127.0.0.1:4000', ws: true },
    },
  },
  build: { outDir: 'dist', sourcemap: true, target: 'es2020' },
});
