---
title: Install Terminal UI
description: Run dcctl install in a full-screen terminal UI that walks you through the checks, the choices and a review before anything is installed.
---

# Install Terminal UI {#install-terminal-ui}

`dcctl install local --ui=tui` runs the cluster install in a full-screen terminal UI. It shows
what the install will do, runs the read-only preflight checks on screen, lets you change the
cluster choices, and asks you to approve the exact plan before anything is installed. It then
shows the install's stages as they run and ends with the result and the next command to run.

```bash
dcctl install local --ui=tui
dcctl install local --ui=tui --kube-context prod
```

The terminal UI is **opt-in** in this release: without `--ui`, `dcctl install` prints, prompts
and exits exactly as it always has, on a terminal or off one. It installs the same thing either
way, with the same flags, the same checks and the same exit status; see
[Install the cluster](./bootstrap.md#install) for what the install does. It covers the cluster
install only. `dcctl bootstrap` is unchanged and still runs as plain text.

## When the terminal UI is used {#when-used}

`--ui` takes one of three values:

| Value | What you get |
| --- | --- |
| `plain` (the default) | The text output `dcctl install` has always printed. |
| `tui` | The terminal UI, when stdin, stdout and stderr are all an interactive terminal. Otherwise plain output, unchanged. |
| `auto` | The terminal UI on an interactive terminal, and plain output everywhere else, including with `--yes` or `--dev`. |

Even with `--ui=tui`, you get plain output, with no escape sequences and no new prompts, when:

- stdin, stdout or stderr is redirected to a file or a pipe;
- the `CI` environment variable is set;
- `TERM` is empty or `dumb` (on Linux and macOS);
- the Windows console cannot process virtual-terminal sequences (legacy console host);
- you pass `--dry-run`, which always prints today's rehearsal output.

With `--ui=tui --yes`, or `--ui=tui --dev`, the terminal UI shows **progress only**: there are no
screens to choose or review, nothing waits for you, and it exits when the install ends.

`--ui=tui` cannot be combined with `--output json`; that is refused before anything runs.

## What does and does not start an install {#what-starts-an-install}

Nothing is installed until you choose **Approve and install** on the review screen. Before then,
the terminal UI only reads: the welcome screen shows the plan, and the preflight checks are the
same read-only checks the plain install runs.

Leaving before you approve changes nothing. Exit, Ctrl+C, an interrupt signal, or the terminal UI
stopping before then all end the command having changed nothing, with exit status 1. If the
terminal UI stops working after the install has started, the same install carries on as plain
output; it is never started a second time.

Approving the review is your answer to everything the plan needs. In particular, if the local
kind cluster does not exist yet, approving also answers the question to create it, for that
cluster and that plan only. Going back and changing anything makes a new plan, which needs its
own checks and its own review.

## The screens {#screens}

### Splash {#splash}

The DeviceChain mark and the dcctl version. It does no work. On a capable local terminal it
animates for at most 1.2 seconds; elsewhere it is a static mark, or a one-line header. Any key
skips it, and that key is not passed on to the next screen. Ctrl+C on the splash exits. Pass
`--no-splash` to leave it out. It is never shown over SSH as an animation, and never with
`--yes`, `--dev`, `--dry-run` or `--help`.

### Welcome {#welcome}

The scope of the command ("This installs shared cluster prerequisites. Create an instance later
with dcctl bootstrap."), the target, footprint, topology and image source as the command line
resolved them, and the words "No changes have been made." Continue runs the preflight checks.

### Preflight {#preflight}

The read-only checks, streaming in as they run. Every check reads as a word: `PASS`, `WARN`,
`FAIL`, `UNKNOWN`, `NOT NEEDED`, `NOT CHECKED` or `INFO`. Move to a check with the arrow keys to
see its fix.

- **Recheck** runs the checks again after you have fixed something.
- **Switch to Compact** (or back to Standard) shows what the change means, then runs every check
  again for the new footprint.
- **Choose another context** picks an existing cluster from the names in your kubeconfig.
- While any check fails, **Continue is unavailable**, and there is no way past it on screen.
  `--skip-preflight` on the command line is still the only way to skip the checks.

#### Existing cluster checks {#preflight-advisory}

With `--kube-context`, an "Existing cluster (advisory)" group follows the checks. It asks the
cluster read-only questions: whether the API server's name resolves, whether it answers over a TLS
certificate the context trusts, whether the context's credentials are accepted, and whether they may
create what the install creates. It also asks whether this machine reaches the provider, chart and
image registries this plan downloads from. These checks are advice: a problem reads `WARN`, anything
they cannot decide reads `UNKNOWN` or `NOT CHECKED`, and they never block Continue.

- **Credential plugins.** A context that gets its credentials from an exec plugin (as GKE, EKS and
  AKS contexts usually do) is authenticated by running the plugin, as kubectl would: never
  interactively, with no terminal and nothing on its input, for at most 10 seconds, and stopped
  together with everything it started when that time is up or you leave the screen. The credential
  it prints is sent only to the context's API server, and is never shown. A plugin that must run
  interactively (`interactiveMode: Always`) is not run and reads `NOT CHECKED`; a plugin that is not
  installed reads `WARN`; one that fails, hangs or prints no usable credential reads `UNKNOWN`. What
  the plugin prints as an error is never shown, since it can hold a token or a key: the row says
  only how the plugin failed, and `kubectl auth whoami` against the context shows its own message.
  Legacy `auth-provider` entries are never run.
- **Registries.** Only the chart repositories of the components this plan installs are checked, so
  Compact (which leaves monitoring out) and the monitoring and CloudNativePG choices in the
  Advanced drawer change the list. The ingress controllers' charts follow the cluster: Traefik's
  always, and ingress-nginx's when the cluster already runs ingress-nginx (which the install keeps
  unless `--remove-legacy-ingress` is given), read from the cluster's IngressClasses.
- **Proxies.** These checks never go through a proxy. When one is configured, for the context or in
  the environment (`HTTPS_PROXY` and the like), the rows it would carry read `NOT CHECKED`.

### Cluster choices {#cluster-choices}

The target (a managed local kind cluster, which you can rename, or an existing context), the
footprint (Standard or Compact) and the topology (Single or HA). A value you set on the command
line is marked with the flag that set it. What Compact leaves out is spelled out under it.

**Advanced** opens a drawer with editors for the monitoring stack, the bundled CloudNativePG
operator and, on Compact, TLS. It says whether database backups will run, as the plan decides
it. Every other flag you gave on the command line is listed there by name and carried into the
install unchanged. Credentials show only as "configured (hidden)".

Each change is checked the way the command line would check it, so a combination the command
line refuses is refused on screen too, and the read-only checks run again for the changed plan.

### Review {#review}

The plan as the command line would resolve it: the target and whether it is created or reused,
the release and image registry, the footprint and topology, TLS, monitoring, backups, any
warnings, and where the cluster's state will be kept. It also shows the **equivalent command**:
the non-interactive `dcctl install` command, with `--yes`, that reproduces this plan. A
credential such as an alert webhook appears in it only as a placeholder, and the command is
then marked as a template.

The review opens with **Back** focused, and keys typed before it has settled are ignored, so an
extra Enter can never approve it.

### Progress {#progress}

The install's stages, each with a status word (`DONE`, `RUN`, `WAIT`, `FAIL`, `STOP`) and its own
bar. A bar is measured only when there is something real to count, such as OpenTofu actions or
replicas ready (`2/3`); otherwise a marker shows that work is under way. The overall bar counts
completed stages out of the stages that apply. It is never a percentage of the install and
never an estimate of the time left, which always reads `Remaining: unknown`. If nothing new has
been observed for a while, the screen says the work is still running; it does not call that
hung. After the last stage it reads `Finalizing` until the install reports its outcome.

Press `l` to open the log pane, which shows the install's output with credentials removed. PgUp
and PgDn scroll it and pause it; `f` or End follows it again.

**Ctrl+C during the install** stops it safely: the screen says "Interrupt received. Waiting for
the operation to stop safely. Nothing is being rolled back." and stays until the install has
stopped. A second Ctrl+C exits at once without waiting, as the plain install does. Esc never
stops the install.

### Result {#result}

- **Success** says "Shared prerequisites installed", with the cluster, its shape, TLS,
  monitoring, backups and connection budget. A cluster has no console of its own, so none is
  shown. Next is the `dcctl bootstrap` command that creates an instance on it. Done exits 0.
- **Failure** names the stage that failed and its cause, what may have been changed, whether
  the cluster's lock was released, a command to inspect the cluster, and the fix. An install
  converges, so the fix is to correct the cause and **run the same command again**; the
  re-run command is shown. **Review for re-run** takes the same request back through the review,
  once the failed run has fully stopped. Nothing is rolled back, and closing the failure screen
  never turns the result into success: dcctl exits 1.
- An install that was **recorded but whose replicas were still joining** when the readiness wait
  ran out is shown as a failure that says so, never as a success.

When dcctl exits, the terminal is restored and a short summary is printed: the outcome, the
target and the next command. It contains no credentials.

## Keys {#keys}

| Key | Before the install | During and after the install |
| --- | --- | --- |
| Tab / Shift+Tab | Move between fields and actions | Move between the result screen's actions |
| ↑ / ↓ | Choose an option, or move between checks | — |
| Space | Toggle a choice | — |
| Enter | Activate the focused action | Activate the focused action on the result screen |
| `y` / `n` | — | Answer a question the install asks on screen (a progress-only run) |
| Esc | Back to the previous screen (your changes are kept) | Close help. Never stops the install |
| `?` / F1 | Help for the screen: its keys and what it does. F1 also works while you type a name | Help, on every screen. It never pauses the install |
| `l`, PgUp / PgDn, `f` / End | — | Open the log pane, scroll it, follow it |
| `q` | Move to Exit | Exit, once the install has ended |
| Ctrl+C | Exit; nothing has been changed | Stop the install safely; a second press exits at once |

The footer of every screen lists the keys that work there, and its help lists exactly the same
keys, with a few words on what the screen does. Help changes nothing: while it is open, Esc, `?`
or F1 closes it, the arrows and PgUp / PgDn scroll it, and every other key does nothing except
Ctrl+C, which keeps its meaning. On a screen whose approval starts something (the review, and
`dcctl setup local`'s hand-over and instance review), opening or closing help counts as a key
press, so the screen waits for the keyboard to be quiet again before it accepts one. Mouse reporting is off, so selecting
and copying text with the mouse works as it does in any terminal. The focused control is marked
with `›` and shown inverted, never by colour alone.

## Terminals and accessibility {#accessibility}

The terminal UI adapts to what the terminal can do. Every state is written as a word, so nothing
depends on colour:

| Terminal | What you see |
| --- | --- |
| Truecolor, Unicode | Full colour, the DeviceChain mark, block-drawn bars |
| 256 colours, Unicode | The same, with the nearest 256-colour palette |
| 16 colours, or Unicode not certain | ASCII borders and bars, a text header instead of the mark, a static `working` marker |
| `NO_COLOR` set | No colour at all, a text header instead of the mark; the words carry every state, and nothing moves |

`NO_COLOR` takes precedence over `CLICOLOR_FORCE`. Over SSH the splash is static. The layout
follows the terminal's size: a full layout from 100×30, a single column from 80 columns, a compact
layout without the outer border from 60 columns. On a terminal smaller than 60×16 the screens
before the install ask you to make the window larger or to use `--ui=plain`; during the install a
minimal status block is shown. Resizing the window never cancels or restarts anything.

**For a screen reader, use `--ui=plain`.** The terminal UI redraws the whole screen, which screen
readers do not follow well, and it is not designed for them. `--ui=plain`, the default, is the
same install as ordinary lines of text and prompts, and it is permanent: it stays available
whatever the default becomes.

On Windows the terminal UI needs a console that processes virtual-terminal sequences: Windows
Terminal, or the console host of current Windows 10 and 11, with PowerShell or `cmd`. It needs no
Bash, WSL or other tools. On a legacy console it falls back to plain output. On every exit,
including Ctrl+C and errors, the console's modes are put back as they were.

## Exit status {#exit-status}

The exit status is the same as the plain install's: 0 when the install succeeded, 1 when it
failed, was refused, or was stopped. Leaving a screen never changes it.

## Cluster, then instance: `dcctl setup local` (Preview) {#setup-local}

`dcctl setup local` runs the cluster install and then the bootstrap of a first instance in one
guided session. It is in Preview. `dcctl install` and `dcctl bootstrap` are unchanged by it and
remain the way to script either step.

It has two stages, each with its own review and its own approval:

1. **The cluster.** The same screens as `dcctl install local --ui=tui`, from the welcome to the
   review. **Approve and install** installs the cluster. When it has finished, Enter continues.
2. **The instance.** A hand-over screen says the cluster is installed and shows the
   `dcctl bootstrap` command that creates an instance later. **Continue to the instance** leads to
   the instance and root-key escrow choices, then to the instance's own review.
   **Approve and create the instance** starts the bootstrap. The escrow passphrase is asked for on
   screen when the run reaches its escrow step, and is never shown. After a successful run come the
   escrow receipt, the one-time first-login password and the instance summary.

Nothing of the instance runs without its own approval, and the cluster's approval answers nothing
the instance run asks. Every screen that leads on (the hand-over and both reviews) takes no key
until it has been on screen for a moment, so keys typed ahead cannot approve anything.

`dcctl setup local` is guided only. It needs stdin, stdout and stderr to be an interactive terminal
(not CI, not `TERM=dumb`), and it has no `--yes`. Without a terminal it changes nothing and exits 2,
naming the two commands to run instead. Its flags (`--kube-context`, `--cluster`, `--compact`,
`--ha`, `--no-splash`) only set where the screens start; each can be changed on them.

If you leave or the instance fails after the cluster stage, the cluster stays installed. Setup says
so and prints the exact `dcctl bootstrap` command that creates the instance: with the choices you
made, once you made them.

| Exit status | Meaning |
| --- | --- |
| 0 | The cluster is installed and the instance was created |
| 1 | The cluster stage failed, or was stopped after its approval, as `dcctl install` exits 1 |
| 2 | Nothing was changed: stopped before the cluster's approval, or refused before starting |
| 3 | The cluster is installed; no instance was created (left before the instance's approval) |
| 4 | The cluster is installed; creating the instance failed, or was stopped after its approval |
