# video-capture: unattended console recorder for the explainer series

`video_capture.py` reads a **shot list** (YAML) and records each console shot as its own 1080p clip, with a visible cursor,
human-paced typing, secret regions blurred in the page, and a manifest that lets an edit line the clips up with voice-over lines.
It generalises `demos/sitepulse-unity/tools/console_capture.py` (same screencast-to-ffmpeg path, same injected cursor, same
in-process credential handling) and replaces its hard-coded shots with data.

```bash
hack/video-capture/setup.sh                  # once: venv + pinned Playwright/Chromium (ffmpeg must be on PATH)
PY=~/.cache/video-capture/venv/bin/python    # Scripts/python on Windows
$PY hack/video-capture/video_capture.py check  hack/video-capture/shots/poc-device-profile.yaml
$PY hack/video-capture/video_capture.py record hack/video-capture/shots/poc-device-profile.yaml --out ./clips [--shots id,id] [--base-url URL]
```

On Windows pass a Windows path to `--out` (`C:/...`): Git Bash with `MSYS_NO_PATHCONV=1` hands `/c/...` to Python verbatim, which then
means `C:\c\...`. The recorder refuses `/c/...`, a bare `/c` and `/cygdrive/...`.

Exit status: `0` all shots recorded, `1` a shot failed (screenshot written, the rest still run), `2` the shot list or setup is wrong,
`3` the sign-in password appeared in a recorded page, or a password field was on screen without its blur (the run stops, no clip and no screenshot is kept).
A password shorter than 4 characters is refused up front (exit 2): the guard cannot be trusted with it.

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
  mode: sim                      # sim | superuser-secret | env | none
  sim: abt-1000-h                #   sim: ~/.devicechain/sims/<sim>.json (simEmail, simPassword)
  # superuser-secret: {instance: guide, email: superuser@devicechain.local, [namespace], [secret], [kube_context]}
  #   reads Secret dci-<instance>/dci-<instance>-superuser key password with kubectl (honours KUBECONFIG)
  # env: {email_env: DC_EMAIL, password_env: DC_PASSWORD}
  # none: nothing else. The recorder reads no credential and signs in to nothing; shots start signed out (an invitation link, /login,
  #   an SSO return). A `signin` step is refused with it (there is no credential to sign in with). Unknown modes are refused.
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
| `signin` | `dash` | signs in to `/dash` through its own form, see below. `dash` is the only target; anything else is refused by `check` |

### Signing in to /dash

`/dash` keeps its session in the page's memory (not cookies or storage), so the recorder's console sign-in does not carry over; a shot that
starts on `/dash/` lands on its sign-in form. `signin: dash` fills that form (`data-shot` `dash-email`, `dash-password`,
`dash-signin-submit`) from the `login` source, in the shot's own page, on every attempt, and waits for the form to go. The credential is read
in-process exactly as for the console and never printed; a shot list still carries none. It needs a `login` mode other than `none`.

```yaml
  - id: c13-dash
    start: /dash/
    mask: [{shot: dash-email}]           # the email shows in the form when it is typed on camera; the password never needs a mask entry
    steps:
      - signin: dash                     # TRIMMED (default): runs before the clip starts, so no frame of it exists. Must be the first step.
      - wait_for: {shot: dash-load}
  # or, to show a person signing in:
      - signin: dash
        on_camera: true                  # recorded: cursor glides, email then password typed at human pace (pace_ms), then Sign in. Any position.
```

At most one `signin` per shot. Its password is protected the same way as every other secret, and the guard cannot be switched off for it:

- **The field must be masked before the first character.** Every visible `input[type=password]` (shadow DOM included) must carry a blur,
  which the recorder's stylesheet puts on all of them. If a page defeats it (or the stylesheet is missing) the run ends with exit 3 and no
  clip, before anything is typed. The same check runs on **every** shot, `login: none` included, and on every guard pass: a signed-out shot
  that is redirected to a password page whose blur was defeated also ends with exit 3.
- The field must really be `type=password`; a page that turns it into plain text is exit 3 (the per-change report compares every other input
  value with the password). The one thing the report skips is the `value` attribute a controlled React field mirrors onto a password input, which
  draws bullets and is covered by the blur check above.
- The whole guard (password in text, attribute, input value, title; the HTTP and GraphQL error checks) runs before and after **each typed
  character** of an on-camera sign-in.
- A refused credential, a second factor or a tenant picker leaves the form on screen: the shot fails (exit 1) with fixed text and no clip.
  Only accounts with a single membership and no second factor can sign in unattended.
- A `type` step marked `secret: true` registers its text with the guard from the moment it is typed (it must be 4+ characters) and may only
  type into a `type=password` input (anything else is exit 3 before a character goes in), so a signed-out shot that sets a new password is
  covered by the same per-change leak check. Typing is not the whole story: mask what the page shows afterwards.

### Masks (secret regions)

Element masks (`shot`, `testid`, `selector`) are a stylesheet applied before paint: `style: blur` (default, 14 px) or `box`. A `shot` or
`testid` value is a plain token (letters, digits, `_ . : -`) because it goes into an attribute selector. `box` also blacks out images,
canvases and iframes inside the element.

Text masks (`text_regex`, a JavaScript regular expression) are **opaque overlay boxes** placed from `Range.getClientRects()` on every
animation frame. The page's own text nodes are not rewritten, so a framework that owns them keeps working. They are boxes, not blurs, because
a backdrop blur left the text legible in headless Chromium.

```yaml
mask:
  - {shot: user-menu, name: account email}              # an element: shot | testid | selector
  - {text_regex: '[\w.-]+@sim\.devicechain\.local'}     # running text, wherever it appears
  - {selector: ".token", required: false}               # required defaults to true
```

`input[type=password]` is always blurred, and a visible one that is not (a page overriding the blur) ends the run, see above. A **required mask that matched nothing during the shot fails the shot** (a mask that silently
misses is a leak); so does a mask stylesheet that has gone missing from the page.

## Fail-loud checks (all of these end the shot; none leaves a clip)

- **The password, per change.** The page reports every changed text node, attribute value and input value (and the whole document at load)
  to the recorder, which compares each with the password. A toast that shows it for 300 ms is caught; the password is never put in the page.
  When it appears the run stops with exit 3, and the temporary frames are deleted. The failure screenshot of any other failure is taken only
  after the same guard passes.
- **HTTP errors.** A same-origin XHR/fetch/document response of 400 or more, and a GraphQL response body that carries `errors`, fail the
  shot (checked after every step).
- A start page that is an error page, a redirect to the sign-in at any point, a `wait_for` that times out (screenshot, then one retry), a
  clip longer than `max_s`.
- Stale output from an earlier run (the clip and failure screenshot of a selected shot) is removed first; clips are encoded to a temporary
  name and renamed; frames live under `<out>/.frames-<pid>` and are swept at the next start if a run was killed.

Known limits: the guard compares the whole password, so a page echoing only a prefix is not seen; a page that reads the password and draws it
onto a canvas is not seen (the page is the product under test, not an adversary); closed shadow roots are not inspected;a page that paints one frame from the compositor thread (a fast scroll) can show a moved text box one frame before its overlay
moves; shadow-DOM text is not reported to the leak check; a GraphQL body that the browser has already discarded cannot be read.

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

`python3 -m unittest -v test_video_capture` (pure functions: schema validation, targets, masks, timeline, credentials, scrubbing); no browser,
this is what CI runs.

`cd hack/video-capture && DC_BROWSER_TESTS=1 <venv python> -m unittest -v test_browser_paths` (maintainer-run; about 3 minutes, one Chromium
per case; run it whenever `video_capture.py` changes) runs the real recorder against a throwaway local web server and judges each
negative control by what it leaves behind: a planted password (a transient toast, an attribute) is exit 3 with no mp4 and no screenshot; a
required mask that matches nothing, a 404 start page, a 500 fetch and a GraphQL `errors` body are exit 1 with no mp4; a short password is exit 2;
a good shot is exit 0 with both mask kinds. It needs Chromium and ffmpeg (`setup.sh`), so CI does not run it. To see the leak tests fail, make
`_on_text` a no-op and `password_leaked` return False: both exit-3 tests then fail with exit 0 and a clip kept.

The sign-in controls (`login: none`, `signin: dash`): a good signed-out shot exits 0 and the server counts **no** login POST; a signed-out shot
redirected to a password page with the blur defeated, and a `/dash` sign-in (on camera and trimmed) on a page that defeats the password blur, are exit 3 with no mp4 and
no screenshot; a `/dash` whose sign-in answers 401 is exit 1 with no mp4; a good `/dash` sign-in, on camera and trimmed, is exit 0 and the password is in no output file. To see
them fail, make the `pr["bare"]` test in `guard` unreachable (`if False and pr["bare"]`): the three exit-3 controls then exit 0 and keep a clip.
