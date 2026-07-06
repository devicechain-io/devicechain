// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

/// <reference types="vite/client" />

// @fontsource-variable/inter ships CSS only (no type declarations). Declare it as
// a side-effect module so a bare `import '@fontsource-variable/inter'` typechecks
// under TypeScript 6, which no longer resolves the package's bare export for types.
declare module '@fontsource-variable/inter';