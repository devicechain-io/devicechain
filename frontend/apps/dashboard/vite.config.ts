import react from '@vitejs/plugin-react';
import path from 'path';
import { defineConfig, loadEnv } from 'vite';

// The dashboard app is served same-origin at /dash (behind the shared ingress), so
// its assets must resolve under that base. In dev the /api proxy mirrors the
// console's so the SDK's relative /api/<area>/graphql paths reach the backend.
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '');
  return {
    base: '/dash/',
    plugins: [react()],
    resolve: {
      alias: {
        '@': path.resolve(__dirname, './src'),
      },
    },
    server: {
      port: 5174,
      proxy: {
        '/api': {
          target: env.VITE_GATEWAY_TARGET || 'http://localhost:8080',
          changeOrigin: true,
          rewrite: (p) => p.replace(/^\/api\/[^/]+/, ''),
        },
      },
    },
  };
});
