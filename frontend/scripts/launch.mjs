// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// How every frontend script starts another process. This is the ONLY file under
// scripts/ that may import child_process; launch.test.mjs fails if another one does.
//
// 🔴 WHY NODE CLIs ARE LAUNCHED AS `node <their own .js>`. On Windows, `npm` and
// `node_modules/.bin/tsc` are `.cmd` shims, and a spawn without a shell cannot start
// a `.cmd` — the PATH search only tries `.com` and `.exe`. The builders used to spawn
// both by name, so on native Windows `build:packages` failed before compiling
// anything. `shell: true` is not the fix: it re-parses every argument through cmd.exe.
// Running the tool's own JavaScript entry point under the Node already running this
// script is the same on every platform, and it is exactly what the shims themselves do.
//
// 🔴 A PROCESS THAT DID NOT RUN TO COMPLETION IS AN ERROR, NOT AN EXIT STATUS. spawnSync
// reports a failed launch in `result.error` and leaves `status` null; a process killed
// by a signal also comes back with `status` null, with `signal` set and NO error. The
// builders read only `status`, so all a Windows contributor saw was "exit null" — and a
// caller that reads `status !== 0` as "npm said no" turns "npm is not there" (or "npm
// was killed") into a wrong answer. runSync() throws for both instead, so every status
// a caller sees is a number the tool itself chose to exit with.

import { spawnSync } from 'node:child_process';
import { existsSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { PackageError } from './packages.mjs';

// A subclass of PackageError, so every existing `catch (err instanceof PackageError)`
// in the release scripts reports it as a named failure rather than a stack trace.
export class LaunchError extends PackageError {}

/** @typedef {{ file: string, args: string[] }} Command */

/** A Node script run under the current Node. */
export function nodeCommand(script, args = [], { execPath = process.execPath } = {}) {
  return { file: execPath, args: [script, ...args] };
}

// npm sets npm_execpath to its own bin/npm-cli.js for every script it runs. pnpm and
// yarn set it to THEIR CLI, which is not npm, so the basename is checked.
const NPM_CLI = /^npm-cli\.(c?js|mjs)$/;

function bundledNpmCli(execPath, platform) {
  // Node's installers put npm in different places relative to the node binary:
  // beside it on Windows (C:\Program Files\nodejs\node_modules\npm), under the
  // prefix's lib/ everywhere else (<prefix>/bin/node, <prefix>/lib/node_modules/npm).
  // The platform's own path module, so the Windows layout is testable on Linux.
  const p = platform === 'win32' ? path.win32 : path.posix;
  const dir = p.dirname(execPath);
  return platform === 'win32'
    ? p.join(dir, 'node_modules', 'npm', 'bin', 'npm-cli.js')
    : p.join(dir, '..', 'lib', 'node_modules', 'npm', 'bin', 'npm-cli.js');
}

/**
 * The npm CLI script to run under Node, or null when there is none to be found:
 *   1. npm_execpath, when it is npm's — the npm the user actually invoked;
 *   2. the npm bundled with the running Node — what release.yml gets, since it runs
 *      `node scripts/...` directly and npm_execpath is then unset.
 */
export function resolveNpmCli({
  env = process.env,
  execPath = process.execPath,
  platform = process.platform,
  exists = existsSync,
} = {}) {
  const fromEnv = env.npm_execpath;
  if (fromEnv) {
    const base = (platform === 'win32' ? path.win32 : path.posix).basename(fromEnv);
    if (NPM_CLI.test(base) && exists(fromEnv)) return fromEnv;
  }
  const bundled = bundledNpmCli(execPath, platform);
  return exists(bundled) ? bundled : null;
}

/** npm, launched through Node. Options as resolveNpmCli. */
export function npmCommand(args, opts = {}) {
  const { env = process.env, execPath = process.execPath, platform = process.platform } = opts;
  const cli = resolveNpmCli(opts);
  if (cli) return { file: execPath, args: [cli, ...args] };
  // POSIX keeps today's PATH lookup: `npm` is a real script there, and this is what
  // keeps a distro-packaged npm (installed somewhere other than beside node) working
  // for `node scripts/x.mjs`. On Windows a bare `npm` cannot be spawned, so say why.
  if (platform !== 'win32') return { file: 'npm', args };
  throw new LaunchError(
    `cannot find npm's CLI: npm_execpath is ${env.npm_execpath ? `'${env.npm_execpath}' (not npm-cli.js)` : 'unset'} ` +
      `and there is no ${bundledNpmCli(execPath, platform)}. Run this through npm (e.g. \`npm run build:packages\`).`,
  );
}

/**
 * The TypeScript compiler, launched through Node. Found by walking up from `from`
 * through each directory's node_modules — the workspace's own install and nothing
 * else. Deliberately NOT require.resolve: that also consults NODE_PATH and the
 * global folders, which would let a box with no `npm ci` but a global TypeScript
 * build with an unpinned compiler instead of failing.
 *
 * The entry is the manifest's own `bin.tsc` — what the POSIX `.bin/tsc` symlink and
 * the Windows `tsc.cmd` shim both run. `typescript/lib/tsc.js` is not used: the
 * package's `exports` does not export it, so resolving it by name fails.
 */
export function tscCommand(args, { from, execPath = process.execPath } = {}) {
  let manifestPath = null;
  for (let dir = path.resolve(from); ; ) {
    const candidate = path.join(dir, 'node_modules', 'typescript', 'package.json');
    if (existsSync(candidate)) {
      manifestPath = candidate;
      break;
    }
    const parent = path.dirname(dir);
    if (parent === dir) break;
    dir = parent;
  }
  if (!manifestPath) {
    throw new LaunchError(`typescript is not installed in any node_modules above ${from} — run \`npm ci\` in frontend/`);
  }
  const bin = JSON.parse(readFileSync(manifestPath, 'utf8')).bin;
  const rel = bin && typeof bin === 'object' ? bin.tsc : undefined;
  if (typeof rel !== 'string') {
    throw new LaunchError(`${manifestPath} declares no bin.tsc — this is not the compiler this build pins`);
  }
  return { file: execPath, args: [path.resolve(path.dirname(manifestPath), rel), ...args] };
}

// The native programs a script may start by name. An ALLOWLIST, not a list of Node
// CLIs to refuse: a refusal list is only as good as its author's memory of every shim
// npm can link (npm, npx, tsc, vite, eslint...), and the name it forgets is the bug
// this file exists to prevent — invisible on Linux, fatal on Windows. Add a program
// here only if it is a real executable on every platform the scripts run on.
export const NATIVE_PROGRAMS = Object.freeze(['tar']);

/**
 * A NATIVE executable, found on PATH — and only one named in NATIVE_PROGRAMS. It exists
 * so that every launch goes through this file and the guard in launch.test.mjs has no
 * exception to make. A Node CLI (npm, npx, tsc, vite...) is a `.cmd` shim on Windows,
 * so asking for one here THROWS: use nodeCommand / npmCommand / tscCommand.
 */
export function systemCommand(file, args = []) {
  if (!NATIVE_PROGRAMS.includes(file)) {
    throw new LaunchError(
      `systemCommand('${file}') refused: only ${NATIVE_PROGRAMS.join(', ')} may be started by name. ` +
        'A Node CLI is a .cmd shim on Windows that a shell-less spawn cannot start; ' +
        'launch it with nodeCommand, npmCommand or tscCommand instead.',
    );
  }
  return { file, args };
}

/** A command as a person would type it, for messages: `npm run build`, not a path to npm-cli.js. */
export function describeCommand({ file, args }) {
  const name = (s) => path.win32.basename(path.posix.basename(s));
  if (args.length > 0 && NPM_CLI.test(name(args[0]))) return ['npm', ...args.slice(1)].join(' ');
  return [name(file), ...args].join(' ');
}

/**
 * spawnSync, except that a process which did not run to completion THROWS. `error` is
 * set when the launch itself failed (ENOENT, EACCES), and also when a process that did
 * start was cut off (ETIMEDOUT, ENOBUFS). A process killed by a signal has no `error`
 * at all — only `status: null` and `signal`. In every one of those `status` says
 * nothing about what the tool decided, so a returned result always carries a numeric
 * status. The caller still owns the status check.
 */
export function runSync(command, options = {}) {
  const result = spawnSync(command.file, command.args, options);
  const where = options.cwd ? ` in ${options.cwd}` : '';
  if (result.error) {
    throw new LaunchError(
      `\`${describeCommand(command)}\` failed to run${where} (${command.file}): ` +
        `${result.error.code ?? 'error'}: ${result.error.message}`,
    );
  }
  if (result.status === null) {
    throw new LaunchError(
      `\`${describeCommand(command)}\` did not run to completion${where}: killed by ${result.signal ?? 'an unknown signal'}`,
    );
  }
  return result;
}
