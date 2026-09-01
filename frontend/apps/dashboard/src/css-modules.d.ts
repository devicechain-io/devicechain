// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Module specifiers Vite understands and TypeScript does not model on its own: a
// stylesheet imported for its SIDE EFFECT, and Vite's `?worker&url` suffix.
//
// 🔴 A SECOND COPY OF THE CONSOLE'S DECLARATIONS, AND THAT IS THE RIGHT SHAPE. An ambient
// `declare module` is global to whatever program includes it, and these two apps are
// compiled by separate tsconfig programs. Hoisting one copy into a shared PACKAGE is the
// move to avoid: it previously lived in `@devicechain/widgets`, where it would have ridden
// into the published `.d.ts` and patched every consumer's module resolution — and, in the
// tree, would have MASKED a stylesheet import accidentally reintroduced into the package,
// hiding the exact regression the dialect gate exists to catch.
//
// So: each app declares the dialect its own bundler speaks. That is a dozen duplicated
// lines and no shared authority over what a bundler means, which is the correct trade.

declare module '*.css' {
  const url: string;
  export default url;
}

declare module '*?worker&url' {
  const url: string;
  export default url;
}
