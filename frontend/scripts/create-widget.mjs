// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const header = '// Copyright The DeviceChain Authors\n// SPDX-License-Identifier: Apache-2.0\n';

/** Create a standalone package; never merge into an existing destination. */
export function createWidget(id, destination) {
  if (typeof id !== 'string' || !/^[a-z0-9]+(?:-[a-z0-9]+)*\/[a-z0-9]+(?:-[a-z0-9]+)*$/.test(id)) {
    throw new Error('Widget id must be lowercase scope/name, for example acme/tank-level.');
  }
  if (!destination) throw new Error('A new destination directory is required.');
  const [scope, name] = id.split('/');
  const sdk = JSON.parse(readFileSync(new URL('../packages/widget-sdk/package.json', import.meta.url), 'utf8'));
  const manifest = {
    name: `@${scope}/widget-${name}`, version: '1.0.0', private: true,
    type: 'module',
    exports: { '.': { types: './dist/index.d.ts', import: './dist/index.js' } },
    files: ['dist'],
    scripts: { build: 'tsc', typecheck: 'tsc --noEmit' },
    peerDependencies: {
      '@devicechain/widget-sdk': sdk.version,
      '@devicechain/dashboards': sdk.peerDependencies['@devicechain/dashboards'],
      react: sdk.peerDependencies.react,
    },
    devDependencies: {
      '@types/react': sdk.devDependencies['@types/react'],
      typescript: sdk.devDependencies.typescript,
    },
  };
  const config = {
    compilerOptions: {
      target: 'ES2020', lib: ['ES2020', 'DOM'], module: 'ESNext', moduleResolution: 'bundler',
      jsx: 'react-jsx', strict: true, skipLibCheck: true, declaration: true,
      rootDir: 'src', outDir: 'dist',
    },
    include: ['src'],
  };
  const source = `${header}
import { WidgetFrame, defineWidget, optString } from '@devicechain/widget-sdk';

export const widget = defineWidget({
  descriptor: {
    id: '${id}', version: '1.0.0', sdkApiVersion: 1, optionsVersion: 1,
    display: { title: '${name}', category: 'Custom' },
    channel: 'none', datasource: { mode: 'none' }, binding: {},
    options: { text: { kind: 'string', doc: 'Text displayed in the card.', label: 'Text' } },
    defaults: { text: 'Hello from ${id}' }, interactions: {},
  },
  implementation: {
    kind: 'trusted-react',
    component: ({ widget }) => (
      <WidgetFrame title="${name}">{optString(widget.options, 'text')}</WidgetFrame>
    ),
  },
});
`;
  // Non-recursive mkdir is intentional: even an empty existing directory is refused.
  mkdirSync(destination);
  mkdirSync(path.join(destination, 'src'));
  for (const [file, contents] of Object.entries({
    'package.json': JSON.stringify(manifest, null, 2) + '\n',
    'tsconfig.json': JSON.stringify(config, null, 2) + '\n',
    'src/index.tsx': source,
    'README.md': `# ${id}\n\nSDK peer ranges come from the checkout. If they are 0.0.0-dev placeholders,\nset the @devicechain/* peer ranges to released versions before installing.\nRun npm install, npm run typecheck, then npm run build.\n\nImport { widget } from '${manifest.name}' and include it in the host's\ncreateWidgetRegistry list. Keep a single copy of the SDK and React in the host.\nThis starter uses the static channel and opens no data connection.\nSee docs/docs/guides/writing-a-custom-widget.md in the DeviceChain repository for data channels.\n\nThe package is private by default. Review its name, version and licensing before publishing.\n`,
  })) writeFileSync(path.join(destination, file), contents, { flag: 'wx' });
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    if (process.argv.length !== 4) throw new Error('Usage: npm run create-widget -- scope/name new-directory');
    createWidget(process.argv[2], process.argv[3]);
    console.log(`Created ${process.argv[2]} in ${process.argv[3]}`);
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
