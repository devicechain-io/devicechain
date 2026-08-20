// defineConfig from vitest/config (a superset of vite's) so the `test` block
// below is typed; it is otherwise the same config Vite consumes for the build.
import { defineConfig } from 'vitest/config';
import { loadEnv, searchForWorkspaceRoot } from 'vite';
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
    // The console is a browser app, so its unit tests default to a DOM
    // environment: anything touching window/localStorage (the i18n config and
    // its browser language detector, ADR-066) needs it, and a per-file
    // `@vitest-environment` docblock proved unreliable across Node versions in
    // CI. Pure-logic tests run fine here too (jsdom is a superset). jsdom is a
    // devDependency of this workspace.
    test: {
      environment: 'jsdom',
      // Force a working in-memory localStorage — newer Node's experimental
      // native global shadows jsdom's and is unavailable without a flag (see the
      // setup file). Runs after the environment is set up, before test files.
      setupFiles: ['./vitest.setup.ts'],
    },
    resolve: {
      alias: {
        '@': path.resolve(__dirname, './src'),
      },
    },
    server: {
      port: 5173,
      fs: {
        // The area-catalog test reads the Helm chart's own `$known` list to check
        // the client's Area vocabulary against the areas the chart can actually
        // deploy, rather than keeping a third copy of that list here for the two
        // to drift away from. The chart sits above the npm workspace root, which
        // Vite denies by default, so it needs an explicit entry.
        //
        // 🔴 searchForWorkspaceRoot is not decoration. Setting `fs.allow` at all
        // REPLACES Vite's automatic workspace-root allowance, so a hand-written
        // list silently revokes access to frontend/packages/* — every page
        // importing @devicechain/{client,dashboards,widgets} then 403s in
        // `npm run dev`. Measured: with allow: ['..', <helm>], a request for
        // /@fs/.../packages/client/src/index.ts returned 403 while the chart
        // returned 200.
        //
        // 🔴 AND IT GOVERNS VITEST TOO, not only the dev server — this comment
        // used to say otherwise and was measured to be wrong. A test importing a
        // path outside the allow list fails with `Error: Denied ID …`, which is
        // how the two backend entries below came to be needed at all.
        //
        // Each entry is the NARROWEST directory that is actually imported, for
        // the same reason the chart entry names deploy/helm rather than deploy:
        // the dev server serves anything reachable under an allowed root via
        // /@fs/, so a whole-tree grant would also serve every untracked local
        // file a developer happens to leave there.
        allow: [
          searchForWorkspaceRoot(process.cwd()),
          path.resolve(__dirname, '../../../deploy/helm'),
          // The rule taxonomy + compiler dispatch (taxonomy-lockstep.test.ts).
          path.resolve(__dirname, '../../../backend/services/event-processing/internal/rules'),
          // The CEL predicate environment (cel-vocabulary.test.ts).
          path.resolve(__dirname, '../../../backend/services/event-processing/internal/detect/predicate'),
        ],
      },
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
