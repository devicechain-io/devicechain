# video-capture: unattended console recorder for the explainer series

`video_capture.py` reads a **shot list** (YAML) and records each console shot as its own 1080p clip, with a visible cursor,
human-paced typing, secret regions blurred in the page, and a manifest that lets an edit line the clips up with voice-over lines.
It generalises `demos/sitepulse-unity/tools/console_capture.py` (same screencast-to-ffmpeg path, same injected cursor, same
in-process credential handling) and replaces its hard-coded shots with data.

```bash
hack/video-capture/setup.sh                  # once: venv + pinned Playwright/Chromium (ffmpeg must be on PATH)
PY=~/.cache/video-capture/venv/bin/python    # Scripts/python on Windows
$PY hack/video-capture/video_capture.py check  hack/video-capture/shots/poc-device-profile.yaml
$PY hack/video-capture/video_capture.py record hack/video-capture/shots/poc-device-profile.yaml --out /tmp/clips [--shots id,id] [--base-url URL]
```

On Windows pass a Windows path to `--out` (`C:/...`): Git Bash with `MSYS_NO_PATHCONV=1` hands `/c/...` to Python verbatim, which then
means `C:\c\...`. The recorder refuses that form.

Exit status: `0` all shots recorded, `1` a shot failed (screenshot written, the rest still run), `2` the shot list or setup is wrong,
`3` the sign-in password was visible in a recorded page (the run stops).

## Output

- `<out>/<shot id>.mp4`: H.264, yuv420p, 1920x1080, constant 30 or 60 fps. No clip is written for a shot that failed.
- `<out>/manifest.json`: per shot `start_utc`, `end_utc`, `duration_s` (after trim and hold), `recorded_s`, `attempts`, `line` (the
  voice-over line it belongs to) and `marks`: every step's time in seconds **from the clip's first frame**, so an edit can place a
  voice-over line or a sound cue on the step it describes. Also `failed` (shot id and reason).
- `<out>/failures/<shot id>.png`: the console as it was when a step could not complete (masks applied).

## Shot list schema (version 1)

Unknown keys are errors everywhere (a typo must not silently do nothing). `check` validates without a browser or a credential.

```yaml
version: 1                       # required
video: poc                       # required; the video's id
base_url: http://localhost:8080  # default http://localhost; $DC_CONSOLE_URL or --base-url wins
fps: 30                          # 30 or 60
viewport: {width: 1920, height: 1080}   # fixed; anything else is rejected
reduce_motion: true              # default true: prefers-reduced-motion + CSS that zeroes animations/transitions/caret blink
locale: en-US                    # default; timezone: UTC default (fixed so dates in the console do not move between machines)
cursor: {highlight: true}        # the injected pointer + click ring
login:                           # where to READ the credential; a shot list never carries one
  mode: sim                      # sim | superuser-secret | env
  sim: abt-1000-h                #   sim: ~/.devicechain/sims/<sim>.json (simEmail, simPassword)
  # superuser-secret: {instance: guide, email: superuser@devicechain.local, [namespace], [secret], [kube_context]}
  #   reads Secret dci-<instance>/dci-<instance>-superuser key password with kubectl (honours KUBECONFIG)
  # env: {email_env: DC_EMAIL, password_env: DC_PASSWORD}
shots:
  - id: profile-metrics          # lowercase kebab-case, unique; the clip's file name
    title: ...                   # free text, copied to the manifest
    line: L03                    # the voice-over line this clip serves; copied to the manifest
    start: /device-profiles      # route loaded (and settled) BEFORE recording starts; a redirect to /login fails the shot
    min_s: 16                    # hold the last frame so the clip is at least this long (after trim)
    max_s: 40                    # fail the shot when it takes longer (a hung wait must not become a 5 minute clip)
    trim: {head_s: 0.0, tail_s: 0.0}
    retries: 1                   # 0-5; a failed attempt restarts the whole shot from `start` in a fresh page
    reduce_motion: true          # per-shot override
    mask: [...]                  # secret regions, below
    steps: [...]                 # below
```

### Targets (what a step acts on)

Exactly one locator, plus optional `nth` (0-based) and `exact`:

| key | meaning |
| --- | --- |
| `shot: user-menu` | the console's `data-shot="user-menu"` anchor (the guide-capture convention; preferred: not translated, not restyled) |
| `testid: x` | `data-testid="x"` |
| `role: tab`, `name: Metrics` | ARIA role with an accessible name; `name: "re:^new .*"` is a case-insensitive regex |
| `text: ...` | visible text (exact by default; `exact: false` for a substring; `re:` prefix for a regex) |
| `label:` / `placeholder:` | form field by label / placeholder |
| `selector: "#m-key"` | raw CSS, the last resort |

### Steps

One verb per step. Optional keys on any step: `timeout_s` (default 20), `pause_ms` (rest after gliding to a target, default 450),
`settle_s` (wait after the action), `label` (the manifest mark text).

| verb | value | does |
| --- | --- | --- |
| `goto` | `/route` | navigates in the recorded clip, waits for the network to idle |
| `click` | target | scrolls into view, glides the cursor there, rests, presses (the click ring shows) |
| `hover` | target | glides and rests |
| `type` | `{into: target, text: "..."}` | clicks the field, then types with seeded, human-ish pacing (`pace_ms`, default 85; the same text types the same way each run). `secret: true` keeps the text out of the manifest |
| `press` | `Escape` | a key |
| `wait_for` | target | fails the attempt if the target is not visible within `timeout_s` |
| `wait` | seconds | a hold |
| `scroll` | `{by: 600}` or `{to: target}` | smooth wheel scroll |
| `mark` | `name` | records a named time in the manifest and does nothing else |

### Masks (secret regions)

A mask is applied inside the page before paint, so the frames never hold the value. `style: blur` (default, 14 px) or `box`.

```yaml
mask:
  - {shot: user-menu, name: account email}              # an element: shot | testid | selector
  - {text_regex: '[\w.-]+@sim\.devicechain\.local'}     # running text, wherever it appears (wrapped in a blurred span)
  - {selector: ".token", required: false}               # required defaults to true
```

`input[type=password]` is always blurred. A **required mask that matched nothing during the shot fails the shot** (a mask that silently
misses is a leak). The recorder also reads the page text after every step and **stops the run (exit 3)** if the sign-in password is in it.

## Secrets: what is guaranteed

The credential is read in-process (sim record, `kubectl get secret`, or the environment), typed into the sign-in form and kept in memory.
It is never printed, logged, written (no browser profile, trace, video or storage-state file), or put in a manifest; every message goes
through `scrub()`; the sign-in is not recorded. What it does NOT cover: a secret the console prints that is neither the password nor
marked in `mask` (a token, a JWT) appears as drawn. Mark it.

## Determinism

Fixed 1920x1080 viewport at scale 1, fixed locale and timezone, `prefers-reduced-motion`, a stylesheet zeroing animation and transition
durations and the caret blink, seeded typing. Remaining non-determinism is the data itself (live values, "x minutes ago"): mask it or
trim it out.

## Tests

`python3 -m unittest discover -s hack/video-capture` (pure functions only: schema validation, targets, masks, timeline, credentials,
scrubbing). They need no browser. The browser path is exercised by recording `shots/poc-device-profile.yaml` against a console.
