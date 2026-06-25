# DeviceChain Web Console

The management UI for DeviceChain — a React + Vite single-page app that talks to
the backend services over GraphQL. This is the launch-slice scaffold: it
authenticates against the user-management RBAC service (ADR-008) and renders the
access-control surfaces (users, roles), with device/event/state pages to follow.

## Stack

- **React 19** + **TypeScript** (strict), bundled with **Vite 6**
- **Tailwind CSS v4** (via `@tailwindcss/vite`) with a zinc shadcn/ui theme
- **shadcn/ui** primitives under `src/components/ui` (Radix + `class-variance-authority`)
- **react-router-dom 7** for routing
- **sonner** for toasts, **lucide-react** for icons
- A small **GraphQL-over-fetch** client (`src/lib/graphql/client.ts`) — no Apollo
- A custom **JWT `AuthProvider`** (`src/auth/`) — login/refresh against user-management

## Getting started

```bash
cd frontend
npm install
cp .env.example .env.local   # adjust VITE_GATEWAY_TARGET if needed
npm run dev                  # http://localhost:5173
```

The dev server proxies `/api/<area>/...` to `VITE_GATEWAY_TARGET` (default
`http://localhost:8080`), stripping the `/api/<area>` prefix so a single locally
run service answers on its plain `/graphql` path. Point `VITE_GATEWAY_TARGET` at
the DeviceChain ingress (deploy/opentofu + deploy/helm) and switch the rewrite in
`vite.config.ts` to strip only `/api` to exercise full multi-service routing.

## How auth works

`login(username, password)` returns an access/refresh JWT pair (the username is
globally unique; the tenant comes from the signed token, not a form field). The
`AuthProvider` persists the pair, decodes the access token for display/routing,
and registers a token getter with the GraphQL client that transparently refreshes
the access token when it nears expiry. The decoded claims are **never** trusted
for authorization — every protected operation is enforced server-side by the
token's authorities.

## Layout

```
src/
  auth/            AuthProvider + useAuth (JWT session)
  components/
    ui/            shadcn/ui primitives (borrowed, import-rewritten)
    ThemeProvider, ThemeToggle
  lib/
    api/           typed GraphQL operations per service
    auth/          JWT decode helpers
    graphql/       fetch-based GraphQL client + token injection
    hooks/         use-query, use-mobile, use-local-storage
  routes/          Login, AppLayout, AppSidebar, NavUser, Dashboard, users/, roles/
```

## Scripts

| Command | Purpose |
| --- | --- |
| `npm run dev` | Start the dev server |
| `npm run build` | Type-check then production build |
| `npm run typecheck` | `tsc --noEmit` |
| `npm run preview` | Preview the production build |

## Adding more shadcn primitives

`components.json` is configured (zinc base, `@/components/ui`), so
`npx shadcn@latest add <component>` drops new primitives straight into
`src/components/ui`.
