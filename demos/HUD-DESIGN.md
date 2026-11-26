<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# The demo HUD: design language and components

Every DeviceChain Unity demo tells its story with the same five pieces, drawn the same way. They live in one shared package,
[`demos/shared/io.devicechain.demo-hud`](shared/io.devicechain.demo-hud), so a new demo uses them instead of drawing its own.
[Harborpulse](harborpulse-unity) is the reference user.

The 3D scene is the hero. The HUD says what kind of picture this is, what the viewer is looking at, what happened and what the
platform did about it, in that order of size, and otherwise stays out of the way.

## The rules

**One visual language.** One type family. One size scale, in pixels of a 1920 x 1080 frame: 34 for a caption, 24 for a chain-card
row, 15 for kickers and marks (18 for the top strip and a row's time cue). One corner radius (10). One scrim. Colour is grammar,
the same in every demo and every video of the series:

| Colour | Means |
| --- | --- |
| amber | an alarm |
| green | confirmed, cleared |
| coral | refused or failed (never an alarm) |
| blue | in progress; the platform's own record |
| cool grey | the scenario speaking, not the platform |

**The HUD is drawn last.** The canvas is a screen-space overlay, composited after the scene and all of its post-processing. A
lamp's bloom, a glow, a halo, a particle: nothing in the world is ever drawn over a panel. World markers (pins, ticks, device
names) are part of the HUD but sit under its panels, and a marker whose place on screen falls on a panel is not drawn at all.

**Panels are nearly solid.** A panel is read over the worst thing the scene can put behind it: a street lamp, a storm sky, bright
water. Its scrim is 94 % opaque, and every text colour of the theme holds at least 4.5:1 contrast on that scrim over pure white.
The card a viewer reads longest (an end card, a final summary) has a 96 % backing and the whole frame darkened to half behind it.
Do not lower these to let more of the scene through; move the panel instead.

**Where things go.**

- *Top strip*: small, top right. `RECORDED · Port Alder · 17:37 · ×1.2`: what the picture is, the place, the scene's clock, and a
  rate chip when a shot does not run at the speed it was recorded (the sign is ×, not the letter x). A shot shown out of order
  says so here ("later that day"). It replaces a full-width bar.
- *Caption box*: top centre, under the strip. As wide as its words and no wider, at most two lines, with a kicker in small
  capitals over it (place and subject) and optional small print under it. A thin rule over the box is blue when the caption is a
  claim the platform's record supports and grey when the scenario is speaking. A caption never swaps its words in place: the box
  fades out (250 ms), stays away (150 ms) and fades back in (250 ms). On a hard cut it goes with the shot.
  **Never put a caption at the bottom of the frame**: bottom centre is where a video player writes its subtitles.
- *Chain card*: bottom right, above a player's control bar (8 % of the frame's height). At most 440 px wide and four rows. It can
  dock top right or bottom left when the action is in its corner; it never enters the centre third of the frame.
- *Alarm pins*: in the world, on the device.
- *End card*: centre, three levels (one large line; a tagline and its sub-line; small print).

**The chain card is cause and effect, one recorded step at a time.**

- The first row is what started it. A scenario trigger is drawn apart: an outlined diamond, grey text, a small `SCENARIO` mark,
  and a hairline under it.
- The platform's steps follow under one `PLATFORM RECORD` header, as filled dots on a spine. The header carries the source for
  every row under it, so no row has a second line.
- Each step shows the recorded time since the step above (`+1 s`): the platform's own latency in real seconds, read from the
  records, not from when the rows happened to be drawn and not scaled by the shot's playback rate.
- A number the sending device counted (not the platform) keeps its own mark, `SENDER'S COUNT`, and a hatched dot.
- A row appears only when its recorded fact is true. It fades and slides in (200 ms, 8 px); the newest row is full white and the
  older ones step back to 70 %. The card fades out after the narration of its last shot. It does not sit through the next beat.
- Row text is written for a viewer: "Start command sent", never a rule key or a field name. A digit in a row is one the row's
  evidence bound, never typed.

**Alarm pins, not beacons.**

- A small teardrop with a "!", its tip half a metre over the device's highest point, always facing the viewer.
- Sized in screen space: about 30 px tall at 1080p, never under 22 (a far shot) or over 38 (a close one). The device stays the
  largest thing near its pin.
- Severity is the recorded severity and never colour alone: a warning is an outline; minor is filled yellow-amber; major is
  filled orange with a light edge; critical is filled deep red, 15 % larger, with a slow two-second breath. A severity the
  recording does not carry is drawn as the weakest, never guessed upwards. Every pin has a thin dark outline and stays fully
  opaque at night and in weather.
- Several alarms on one device are one pin, in the worst severity, with a count badge. No label box.
- A raise is a quick scale-in and one soft ring, once. Nothing spins or repeats.
- A clear cross-fades the pin to a green tick (400 ms), which holds three seconds and fades over one.
- No sky beams and no ring stacks. A soft glow on the ground under the pin is all that is kept for distance.
- A device's name is written only for the subject devices of a shot, at most four, and it is only the name: the pin says there
  is an alarm.

**No engineer labels in a presentation.** Rule keys, field names and source notes ("(WORLD)", "(runner)") belong to an engineer
overlay. A demo keeps them there, and its render audit lists the text it really drew, on the HUD and in the world, so a stray
label is on the record instead of found by eye.

**Story first.** One through-line, a narrator, one caption a beat, no shot under four seconds, one slow camera move a shot, and
hard cuts except where time jumps. Whatever the narrator or a caption says the platform did is bound to recorded evidence, with a
weaker wording ready for a day on which the evidence is not there.

## Using the components in a new demo

Add the package to the demo's `Packages/manifest.json`:

```json
"io.devicechain.demo-hud": "file:../../shared/io.devicechain.demo-hud"
```

and reference its two assemblies from the demo's own: `DeviceChain.Demos.Hud.Model` (engine-free: the theme and the timing and
layout rules, usable from a demo's engine-free model and its tests) and `DeviceChain.Demos.Hud` (the uGUI views).

Make one `StoryHud` for the scene's camera and feed its pieces every frame from the demo's own model. The clock you pass is the
presentation clock every fade runs on: show seconds when rendering frame by frame (the same clock then gives the same frames at
any frame rate), wall seconds live.

```csharp
var hud = new StoryHud(camera);                       // once; hud.Dispose() with the scene

// every frame:
hud.Strip.Set(TopStrip.Text("recorded", "Port Alder", "17:37", TopStrip.Rate(1.2)));
hud.Caption.Set(new CaptionContent("North Quay · the storm drain pump", "The platform starts the pump.", "", CaptionTone.Platform), clockS);
hud.Card.Set(new[] {
    new ChainRow("outage", "East Quay loses power", ChainRowKind.ScenarioTrigger),
    new ChainRow("alarms", "6 absence alarms", ChainRowKind.Platform, cueS: 45),
    new ChainRow("mail", "6 notifications sent", ChainRowKind.Platform, cueS: 8),
}, clockS, exitAtS: null);
hud.End.Set(null, clockS);                            // or an EndCardContent on the last shot

// world markers last, kept off the panels
var panels = hud.Panels();
hud.Pins.Panels = panels.Select(p => new[] { p.Pixels.xMin / Screen.width, p.Pixels.yMin / Screen.height, p.Pixels.xMax / Screen.width, p.Pixels.yMax / Screen.height }).ToArray();
hud.Pins.Draw(
    activeAlarms.Select(a => new PinInput(a.DeviceId, AlarmPins.Severity(a.Severity), a.Count)),
    clockS, camera,
    id => new PinPlace(TopOf(id), GroundUnder(id)),   // null for a device the scene does not draw
    new[] { new DeviceTag("lamp-e3", "Lamp E3") });   // the shot's subjects only
```

- Pass the chain card the rows that are true *now*, in order. It remembers when each first appeared and fades it in; a row you
  stop passing is gone. Compute `cueS` from your records' own timestamps.
- Pass the pin layer the devices with an active alarm *now*. It gives each a raise and, when it stops being passed, a clear and a
  tick. On a new shot call `hud.Caption.Cut()` and `hud.Pins.Settle()`: the old caption goes with the shot, and alarms already
  raised are simply drawn, since a viewer is only shown a change that happens in front of them.
- `hud.Extras` is a layer for the few lines only your demo has (a test banner, a problem line). Build them with `HudUi` and the
  theme's sizes and scrim so they match.
- `hud.DrawnText()` lists every string on screen, for a text audit. `hud.Panels()` gives each panel's rectangle in screen pixels,
  for a pixel check: Harborpulse's renderer samples the text-free margin of every opaque panel in every frame it writes and fails
  the render if any sample is too bright for the theme's text to hold 4.5:1 there, which is what a bloom drawn over a panel, a
  marker on one, or a scrim that is too thin all look like.

The package ships no image or font files: every shape (the pin, the tick, the rounded scrim, the dots) is drawn in code on first
use, and the type is Unity's built-in runtime font.

## Checks

```bash
# the engine-free rules (fades, card rows and cues, pin sizes and styles, the opacity and contrast rule)
cd demos/shared/io.devicechain.demo-hud/Dotnet~ && dotnet restore --locked-mode && dotnet build --no-restore -c Release && dotnet test --no-build -c Release
```

The uGUI views are compiled by Unity with the demo that uses them.
