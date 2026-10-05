<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# DeviceChain demo intro

`io.devicechain.demo-intro` is the animated logo intro that opens every DeviceChain Unity demo.
In about three and a half seconds the DeviceChain mark builds itself out of light, turns, and
lands in the brand lockup, the wordmark rises in, and the frame's inner hexagon opens out onto the
demo's first shot.

The mark is a real 3D object built from the official artwork: at the moment it locks, an
orthographic view of it reproduces `branding/logos/symbol.svg` shape for shape and colour for
colour (see [The lock check](#the-lock-check)).

## Using it in a demo

Add the package to the demo's `Packages/manifest.json` as a local package (the path is relative
to the `Packages` folder):

```json
"io.devicechain.demo-intro": "file:../../shared/io.devicechain.demo-intro"
```

Then either:

- drop the prefab `Runtime/Resources/DeviceChainDemoIntro.prefab` into the demo's first scene.
  It plays when the scene starts and removes itself when it has dissolved; or
- start it from code, for example from a scene's bootstrap:

  ```csharp
  using DeviceChain.Demos;

  DemoIntro.Play(() => StartTheDemo());
  ```

The intro draws over whatever the demo's cameras draw and opens onto it, so the demo needs no
other change: start the scene as usual and let the intro cover its first seconds. The component's
`completed` event (and the `Completed` C# event) fires once the demo is fully visible.

| Setting | Effect |
|---|---|
| `playOnStart` | Play when the scene starts (on for the prefab; off when started with `Play`). |
| `reducedMotion` | Show the still lockup for 1.2 s and fade out over 0.3 s instead of the animation. |
| `skippable` | Any key, click, tap or gamepad button fades the intro out over 0.25 s. |
| `sortingOrder` | Sorting order of the overlay canvas the intro is shown on. |
| `background` | The colour behind the mark. |
| `destroyOnComplete` | Remove the intro's GameObject when it has finished. |
| `DemoIntro.PreferReducedMotion` | Use reduced motion everywhere; the `-reduced-motion` command-line flag sets it. |
| `DemoIntro.Disabled` | Every intro completes at once without drawing. Set it before the scene loads (for example from a `RuntimeInitializeOnLoadMethod(BeforeSceneLoad)` method); the `-no-intro` command-line flag sets it. |

## The choreography

Times are seconds from the start; `IntroTimeline` holds them.

| Beat | Time | What happens |
|---|---|---|
| Rim light | 0.00 – 0.90 | Over a dimly lit background, a thin light traces the frame's outer and inner edges (from the top, the two meeting at the bottom). The mark emerges from the background's colour as the light comes up (0.05 – 0.85). |
| Turn | 0.10 – 1.65 | The frame turns 120° about its own axis and untilts from 12°, against the cube's turn, both easing out (quintic). |
| Spin | 0.10 – 1.75 | The cube turns 450° inside the frame, decelerating but still turning at 190° a second when it reaches the pose. While the mark moves its edges are chamfered, and bright edges bloom. |
| Packets | 0.10 – 1.70 | 48 data packets, motion-blurred streaks 2 – 4 px wide, travel in toward the hexagon's six corners and are absorbed there. |
| Sweep | 0.75 – 1.45 | A band of light sweeps across the faces and catches the chamfered edges. |
| Lock | 1.75 | The cube lands in the isometric pose and, like a damped spring (damping 0.55, 3 Hz), carries about 4.4° past it and settles by 2.00. The shading eases into the exact brand colours over the 0.35 s before the lock (ease in-out sine), and the chamfer, the edge light and the bloom go with it. |
| Pulse | 1.75 – 2.25 | A thin outline of the frame in the brand's #7AB7D9 rises over three frames, then widens to 1.35 times the frame, thins from 3 to 1 px and fades (ease-out quad). |
| Wordmark | 1.90 – 2.30 | The wordmark fades in and rises 12 px into place (ease-out cubic), as `logo.svg` lays it out. It is never scaled. |
| Hold | 2.30 – 3.10 | The full lockup holds. |
| Exit | 3.10 – 3.60 | The wordmark fades out (3.10 – 3.30, ease-in cubic). Then the frame's inner hexagon opens onto the demo (3.25 – 3.60): the opening and the frame grow 25 times (ease-in quartic) while the cube fades, until the demo fills the screen. |

## How it works

- **The model** (`Runtime/Models/devicechain_mark.glb`) holds three meshes, `Frame`, `Cube` and
  `Wordmark`, with the brand colours as vertex colours. It is generated from the official SVG
  files by `ArtSource~/build_mark.py` (see [Rebuilding the model](#rebuilding-the-model)). The
  frame and cube also carry a chamfer that exists only while they move: the mesh is in the
  drawing's shape, with zero-area strips along every edge, and each vertex's chamfer offset is in
  UV sets 1 and 2. The mark shader adds the offset times `_Chamfer`, so at 0 the mesh is the
  drawing.
- **The rig** is built at run time far below the scene (`DemoIntro.RigOrigin`), out of reach of
  the demo's cameras: the model, the rim and edge lights (line renderers), the packets, the lock
  pulse, the exit's opening, a dark radial backdrop and an orthographic camera.
- **The image.** The camera renders into a high-dynamic-range render texture with 8x MSAA and no
  post-processing. The intro then runs its own bloom on it (`Hidden/DeviceChain/Intro/Bloom`,
  after URP's: threshold 1.0, intensity 0.55, scatter 0.7). Turning on URP's post-processing for
  the intro's camera would let the demo's volumes (tonemapping, colour grading) change the lock's
  brand colours, so the intro does not. From the lock on the bloom's intensity is 0 and the image
  is copied unchanged. The image has premultiplied alpha and is shown full screen on a Screen
  Space - Overlay canvas; where the exit has opened it is clear, and the demo shows through.
  Nothing in the demo's look reaches the intro.
- **The shaders** (`Runtime/Shaders`): `DeviceChain/Intro/Mark` shades the faces with the intro's
  own neutral key light from 45°, a #9ACEEC fresnel rim and the light sweep, taking each face's
  normal from the surface it draws (so the chamfer's strips are lit), and crossfades that to the
  vertex colour alone (`_Flat`); `DeviceChain/Intro/Glow` is the additive light for the lines,
  the packets and the dithered backdrop; `DeviceChain/Intro/Hole` clears the exit's opening; and
  `Hidden/DeviceChain/Intro/Overlay` shows the image on the canvas.
- **What is shown is a pure function of time.** `IntroTimeline.Evaluate(t)` returns every
  parameter of the frame at `t`; `DemoIntro.Evaluate(t)` poses the rig there without playing and
  `DemoIntro.RenderInto` renders it, which is how frame sequences are captured at a fixed
  timestep.

## Rebuilding the model

The model is code, like the Sitepulse machines. With Blender 4.5 LTS, from `ArtSource~/`:

```
blender --background --python build_mark.py
```

It reads `branding/logos/symbol.svg` and `branding/logos/logo.svg` from the repository and writes
`../Runtime/Models/devicechain_mark.glb` (`--logos DIR` and `--out FILE` override both). How the
flat drawing becomes an object is described at the top of the script. Then, in the Editor, run
**Tools > DeviceChain > Rebuild Demo Intro Assets**, which resets the materials to their
shaders' defaults and writes the prefab, and run the tests.

## The lock check

`ArtSource~/lock_check.py` checks that the locked mark is the drawing. In the Editor,
**Tools > DeviceChain > Render Demo Intro Lock Pose** renders the locked pose (during the hold,
through the same high-dynamic-range image and bloom pass as the intro) orthographically over
`symbol.svg`'s view box (1024 x 1024, black background, the mark alone) to
`Temp/demo-intro-lock.png`; then:

```
python3 ArtSource~/lock_check.py <project>/Temp/demo-intro-lock.png
```

It rasterises `symbol.svg` itself and reports the silhouette's intersection over union, and for
every visible face of the drawing the median rendered colour against the face's colour. It passes
at an IoU of at least 0.98 with every face within 2/255 per channel. On the current model it
reports an IoU of 0.996 and every face exact (median and maximum difference 0 over every
face's interior). A pose rendered 0.1 s before the lock fails it (IoU 0.964, a face's median off
by 12/255), as does one 0.3 s before (IoU 0.896, off by 124/255).

## Tests

`Tests/Editor` runs in Edit Mode (the demo lists the package under `testables` in its
manifest). The timeline: the beats are in order, the lockup holds 0.8 s and the intro lasts
about 3.6 s; it opens on a lit background with the rim starting at once and the mark lit by half
a second; the pose is exactly the isometric one, flat, unchamfered and without glow or bloom from
the settle through the hold; the cube turns at least 2.5° a frame until the lock, carries 3.5 –
5.5° past it and is exact from the settle; the frame tilts and turns against the cube; the colours
ease in over 0.35 s with no frame-to-frame snap; the pulse rises over three frames, then widens,
thins and fades; the exit fades the wordmark before the opening grows, never fades the whole
image into a ghost, and opens wider than a 21:9 screen; reduced motion is the still lockup with
no pulse, packets, bloom or opening. The model: it uses exactly the brand colours, has the
drawing's proportions, and its chamfer has no area in the drawing's shape and a visible width
while moving. The component: it builds and removes its rig, never scales the wordmark, renders an
image that covers the screen during the hold and is clear (and empty) at the end, skips with a
fade, completes at once when disabled, and takes reduced motion from either switch.

## Known limitations

- The intro has no sound.
- Reduced motion is a setting and a command-line flag: Unity does not report the operating
  system's reduce-motion preference on desktop.
- While the intro covers the screen the demo behind it is still running and rendering.
- The bloom is the intro's own pass rather than URP's, so a project-wide change to URP's bloom
  does not reach it.
