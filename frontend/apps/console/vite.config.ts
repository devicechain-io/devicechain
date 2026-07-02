import { defineConfig, loadEnv } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';
import path from 'path';

// The frontend talks to the DeviceChain services over GraphQL. Each functional
// area serves its own /graphql endpoint; the cluster ingress (deploy/helm
// ingress) exposes them at https://<host>/api/<area>/graphql and strips the
// /api/<area> prefix before the request reaches the service, while serving the
// built SPA at "/".
//
// In dev we mirror that contract: the GraphQL client builds URLs as
// `/api/<area>/graphql`, and this proxy forwards `/api/<area>/...` to a backend,
// stripping the `/api/<area>` prefix so a single locally-run service (which
// serves plain `/graphql`) answers. To exercise full multi-service routing,
// point VITE_GATEWAY_TARGET at a real instance's ingress and drop the rewrite
// below (the ingress speaks the same `/api/<area>` contract, so the path passes
// through unchanged).
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
