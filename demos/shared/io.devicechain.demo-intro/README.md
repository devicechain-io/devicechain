<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# DeviceChain demo intro

`io.devicechain.demo-intro` is the animated logo intro that opens every DeviceChain Unity demo.
In about three and a half seconds the DeviceChain mark builds itself out of light, turns, and
locks into the brand lockup, the wordmark fades in and out, and the frame's inner hexagon opens
out onto the demo's first shot.

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
  It plays when the scene starts and removes itself when it has opened onto the demo; or
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
| `destroyOnComplete` | Remove the intro's GameObject when it has opened onto the demo. |
| `DemoIntro.PreferReducedMotion` | Use reduced motion everywhere; the `-reduced-motion` command-line flag sets it. |
| `DemoIntro.Disabled` | Every intro completes at once without drawing. Set it before the scene loads (for example from a `RuntimeInitializeOnLoadMethod(BeforeSceneLoad)` method); the `-no-intro` command-line flag sets it. |

## The choreography

Times are seconds from the start; `IntroTimeline` holds them.

| Beat | Time | What happens |
|---|---|---|
| Rim light | 0.10 – 1.15 | From black, a thin light traces the frame's outer and inner edges (from the top, the two meeting at the bottom) while the frame turns into view. The mark is lit from 0.45 s. |
| Spin | 0.35 – 2.00 | The cube turns inside the frame on its own, slower axis; the edges glow softly. |
| Sweep | 1.05 – 1.85 | A band of light sweeps across the faces. |
| Lock | 2.00 | Frame and cube decelerate and catch in the isometric pose. Over the last 0.12 s the shading flattens to the exact brand colours; a pulse grows out of the frame and fades (2.00 – 2.60). The glow fades out. |
| Wordmark | 2.15 – 2.55 | The wordmark fades in below the mark and rises into place, as `logo.svg` lays it out. |
| Hold | 2.55 – 3.05 | The full lockup holds. |
| Wordmark out | 3.05 – 3.25 | The wordmark fades out where it stands. |
| Opening | 3.25 – 3.60 | The frame's inner hexagon opens: the demo shows through it while the frame grows out past the screen's corners and the cube fades. The intro never fades as one layer, so nothing of it lingers as a translucent ghost. |

## How it works

- **The model** (`Runtime/Models/devicechain_mark.glb`) holds three meshes, `Frame`, `Cube` and
  `Wordmark`, with the brand colours as vertex colours. It is generated from the official SVG
  files by `ArtSource~/build_mark.py` (see [Rebuilding the model](#rebuilding-the-model)).
- **The rig** is built at run time far below the scene (`DemoIntro.RigOrigin`), out of reach of
  the demo's cameras: the model, the rim and edge lights (line renderers), a dark radial
  backdrop, the exit's opening and an orthographic camera.
- **The camera** renders into a render texture with 8x MSAA and no post-processing, shown full
  screen on a Screen Space - Overlay canvas. Nothing in the demo's look (tonemapping, bloom,
  colour grading, ambient occlusion) reaches the intro. The image carries premultiplied alpha:
  the exit's opening (`DeviceChain/Intro/Hole`) clears it, and the canvas
  (`Hidden/DeviceChain/Intro/Overlay`) shows the demo wherever it is clear. The mark draws in the transparent range of the queue (writing depth), so renderer
  features that act after the opaque pass leave it alone.
- **The shaders** (`Runtime/Shaders`): `DeviceChain/Intro/Mark` shades the faces with the intro's
  own key light, a fresnel rim and the light sweep, and crossfades that to the vertex colour alone
  (`_Flat`); `DeviceChain/Intro/Glow` is the additive light for the lines and the backdrop.
- **What is shown is a pure function of time.** `IntroTimeline.Evaluate(t)` returns every
  parameter of the frame at `t`; `DemoIntro.Evaluate(t)` poses the rig there without playing, which
  is how frame sequences are captured at a fixed timestep.

## Rebuilding the model

The model is code, like the Sitepulse machines. With Blender 4.5 LTS, from `ArtSource~/`:

```
blender --background --python build_mark.py
```

It reads `branding/logos/symbol.svg` and `branding/logos/logo.svg` from the repository and writes
`../Runtime/Models/devicechain_mark.glb` (`--logos DIR` and `--out FILE` override both). How the
flat drawing becomes an object is described at the top of the script. Then, in the Editor, run
**Tools > DeviceChain > Rebuild Demo Intro Assets**, which writes the materials and the
prefab, and run the tests.

## The lock check

`ArtSource~/lock_check.py` checks that the locked mark is the drawing. In the Editor,
**Tools > DeviceChain > Render Demo Intro Lock Pose** renders the lock pose orthographically over
`symbol.svg`'s view box (1024 x 1024, black background, the mark alone) to
`Temp/demo-intro-lock.png`; then:

```
python3 ArtSource~/lock_check.py <project>/Temp/demo-intro-lock.png
```

It rasterises `symbol.svg` itself and reports the silhouette's intersection over union, and for
every visible face of the drawing the median rendered colour against the face's colour. It passes
at an IoU of at least 0.98 with every face within 2/255 per channel. On the current model it
reports an IoU of 0.997 and every face's median exact (a difference of 0); a pose rendered 0.1 s
before the lock fails it (IoU 0.987, faces off by up to 87/255), as does one 0.3 s before
(IoU 0.958).

## Tests

`Tests/Editor` runs in Edit Mode (the demo lists the package under `testables` in its
manifest): the beats are in order and the intro lasts about 3.6 s; the pose is exactly the
isometric one with flat colours from the lock to the end, and in motion and lit before it; the
intro starts from black; the rig draws nothing but the mark, its lights, the backdrop and the
opening (no particles); the wordmark fades out where it stands and has gone before the opening
starts; the image covers the whole screen until then, and at the end it is clear, showing the
demo, with nothing of the intro left in it; reduced motion is the still
lockup; the model uses exactly the brand colours and has the drawing's proportions; and the
component builds and removes its rig, skips with a fade, completes at once when disabled, and
takes reduced motion from either switch.

## Known limitations

- The intro has no sound.
- Reduced motion is a setting and a command-line flag: Unity does not report the operating
  system's reduce-motion preference on desktop.
- While the intro covers the screen the demo behind it is still running and rendering.
- The rim light and glow are drawn as geometry rather than with a bloom pass, so they
  do not bleed into the faces the way a bloom would.
