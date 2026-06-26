import { defineConfig, loadEnv } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';
import path from 'path';

// The frontend talks to the DeviceChain services over GraphQL. Each functional
// area serves its own /graphql endpoint; the ingress (deploy/opentofu +
// deploy/helm ingress) exposes them at https://<host>/<area>/graphql and strips
// the /<area> prefix before the request reaches the service.
//
// In dev we mirror that contract: the GraphQL client builds URLs as
// `/api/<area>/graphql`, and this proxy forwards `/api/<area>/...` to a backend,
// stripping the `/api/<area>` prefix so a single locally-run service (which
// serves plain `/graphql`) answers. Point VITE_GATEWAY_TARGET at the ingress
// instead and switch the rewrite to strip only `/api` to exercise the full
// multi-service routing.
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '');
  return {
    plugins: [react(), tailwindcss()],
    resolve: {
      alias: {
        '@': path.resolve(__dirname, './src'),
      },
    },
    server: {
      port: 5173,
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
