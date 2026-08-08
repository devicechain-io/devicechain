# MQTT device plane — native player verification

> ## ✅ PASSED — 2026-08-08
>
> A **native Windows player on IL2CPP** completed the whole device plane against a live cluster:
>
> ```
> [dc] starting — scripting backend: IL2CPP (this is the gate)
> [dc] READY — connected, authenticated, command subscribe GRANTED
> [dc] PUBLISHED a measurement (the broker PUBACKed it)
> [dc] COMMAND received: name=raiseBucket token=il2cpp-gate-132207
> ```
> ```
> sentTime 17:23:01.375 · respondedTime 17:23:01.397 (+22ms) · status SUCCESSFUL
> responsePayload "acknowledged by the Unity player"
> ```
>
> **Unity 6000.5.3f1 · IL2CPP · StandaloneWindows64 · .NET Standard · Managed Stripping `Low` · URP.**
> A pass is a statement about that combination and nothing wider — rerun this page for any other one.
>
> What it retires: IL2CPP runs MQTTnet's socket and buffer code, the hand-built pinned-CA X.509
> validator, source-generated JSON and the async state machines across a background dispatch thread,
> with nothing stripped and no `MissingMethodException`. **The hand-rolled MQTT 3.1.1 fallback the
> spec held in reserve is not needed.** The `IMqttConnection` seam keeps its other virtues; it is no
> longer an escape hatch.
>
> Two platform defects were found by running this page, both since fixed: a Unity/Mono player could
> not reach a broker its own host could (dual-stack resolution), and every command response was
> rejected by a JSON column, stranding commands in `SENT` forever.

**This is the only check that closes the IL2CPP gap, and nothing in CI can substitute for it.**

The C# SDK's MQTT client is gated three ways in CI: unit tests over a fake transport, the real
MQTTnet adapter driven against a scripted TCP broker and against a real `nats-server` MQTT gateway,
and a Native-AOT publish that is **run**, not merely built. All three are strong evidence and none
of them is proof about Unity:

| Rung | Runs on | Says nothing about |
| --- | --- | --- |
| `dotnet test` | CoreCLR, JIT | any AOT compiler |
| Native-AOT gate | ILCompiler | IL2CPP — a *different* AOT compiler |
| `unity-compile-check` | Roslyn, reference assemblies | anything at runtime; it never executes |

So a fully green build tells you the code is correct and AOT-shaped. It does not tell you a Unity
player can open a TLS socket, run MQTTnet's buffer code under IL2CPP's generic sharing, or complete
a command round trip. **Until the steps below have been run for a given Unity version and platform,
the MQTT lane is CI-complete and player-unverified for it.** Say it that way; do not round it up.

That wording is not caution for its own sake: running this page found two real defects that every
CI rung passed straight over, and one of them could only be seen from a player at all.

## The rule that governs this page

**Validate the transport against the smallest possible scene, never against the new one.** The first
player build that exercises MQTT should be the spinning-logo sample plus a single command receiver —
not the construction site. Debugging one unproven thing beats debugging two, and this rule is here
because the WebGL path was learned the expensive way.

---

## 0. Prerequisites

- A local DeviceChain cluster (`deploy/local/`), bootstrapped. **A full `dcctl bootstrap` is all you
  need — there is no separate step and no port-forward.** On a context matching the local heuristic
  (`kind-`, `minikube`, `k3d-`, `docker-desktop`, `rancher-desktop`) `dcctl` passes
  `nats_mqtt_node_port=31883`, which creates a NodePort Service alongside the chart's ClusterIP one,
  and the kind config it embeds maps host `1883` → node `31883`. The broker answers at
  **`ssl://localhost:1883`**.

  > 🔴 **The one way this breaks is a kind cluster that predates the host map.** `dcctl bootstrap`
  > *reuses* an existing `kind-<instance>` cluster ("Using existing kind cluster") rather than
  > recreating it, and **kind fixes `extraPortMappings` at cluster-create time — they cannot be added
  > to a running cluster.** So the NodePort Service appears, `kubectl get svc` looks perfect, and
  > host `:1883` is still a dead route. The symptom is a connection reset / EOF, not a refusal. Check
  > the binding itself rather than the Service:
  >
  > ```bash
  > docker inspect <instance>-control-plane --format '{{json .NetworkSettings.Ports}}' | grep 31883
  > # want: "31883/tcp":[{"HostIp":"0.0.0.0","HostPort":"1883"}]
  > ```
  >
  > If it is absent, `kind delete cluster --name <instance>` and let `dcctl bootstrap` recreate it.

  Then confirm the route end to end — it answers TLS, not plaintext:

  ```bash
  timeout 2 bash -c 'cat < /dev/null > /dev/tcp/127.0.0.1/1883' && echo "TCP reachable"
  timeout 8 openssl s_client -connect localhost:1883 </dev/null 2>&1 | grep -E "subject=|Cipher is"
  # want: subject=... CN = dc-nats.dc-system   and a negotiated cipher
  ```

- **The broker's CA**, so the player can pin it. The bring-up mints a private CA; extract it once:

  ```bash
  kubectl -n dc-system get secret dc-nats-tls -o jsonpath='{.data.ca\.crt}' | base64 -d > nats-ca.pem
  ```

  > 🔑 **Use `localhost`, never `127.0.0.1`.** Measured against a live bring-up: the broker leaf
  > carries `DNS:dc-nats`, `DNS:dc-nats.dc-system`, `DNS:dc-nats.dc-system.svc`,
  > `DNS:dc-nats.dc-system.svc.cluster.local`, `DNS:localhost` — and **no IP SAN at all.** An IP
  > literal is matched only against IP SANs, so `ssl://127.0.0.1:1883` is a hostname mismatch that
  > `PinnedCa` correctly refuses (`ANameMismatchIsRefusedEvenUnderThePinnedRoot` pins that
  > behaviour). Verified both ways against the extracted CA:
  >
  > | Target | `openssl` verdict |
  > | --- | --- |
  > | `localhost` | `Verify return code: 0 (ok)` |
  > | `127.0.0.1` | `Verify return code: 64 (IP address mismatch)` |
  >
  > This matters beyond a URI typo: reaching for the accept-anything mode to get past the mismatch
  > would leave the pinned-trust path — the one piece of security logic hand-built because the
  > one-line API is missing on `netstandard2.1` — unexercised by the only check that runs under
  > IL2CPP.

- A provisioned device, and its **credential id**. The session needs `instanceId`, `tenant`,
  `deviceToken`, `credentialId` — nothing else.
- Unity 6 (the version `VERIFICATION.md` records) with **IL2CPP** and the platform module for your
  target. Mono will *not* answer the question this page exists to ask.

## 0.5 Prove the transport on the desktop first

**Do not open Unity until this passes.** The runbook's own rule — validate against the smallest
possible scene — has a smaller rung than a scene: a console app. `DeviceChain.Sdk.TrustProbe` drives
the shipped transport against your live broker and separates the two things that otherwise fail
identically from a player log ("it didn't connect"): a TLS/pinning problem and an auth problem.

```bash
# A dotnet installed by dotnet-install.sh lands in ~/.dotnet and is NOT on PATH; the shell then
# suggests `snap install dotnet`, which would put a SECOND SDK on the box. Check before installing:
#   ls ~/.dotnet/dotnet && ~/.dotnet/dotnet --version
export PATH="$HOME/.dotnet:$PATH"

# --project is relative to the CWD, so run this from the repository root.
cd /path/to/devicechain

kubectl -n dc-system get secret dc-nats-tls -o jsonpath='{.data.ca\.crt}' | base64 -d > /tmp/ca.pem
dotnet run --project sdks/csharp/tools/DeviceChain.Sdk.TrustProbe -- /tmp/ca.pem
```

> 🔴 **`MSBUILD : error MSB1009: Project file does not exist` means you are not at the root**, not
> that anything is missing. Watch for one specific way of being somewhere else: **`.agent-os` is a
> symlink to a separate repository**, so a shell sitting there looks like it is inside this tree and
> is not — and `git rev-parse --show-toplevel` confirms the wrong root rather than catching it. `pwd -P`
> is the honest check.

It provisions nothing and connects with a deliberately bogus credential, so the *best* outcome is a
completed TLS handshake followed by the broker's own refusal. Expect `4/4 as expected`:

| | case | expected |
| --- | --- | --- |
| A | `localhost` + correct pinned CA | TLS **OK**, then `NotAuthorized` at CONNACK |
| A′ | `localhost` + accept-any | the *same* refusal — so A's failure was auth, not TLS |
| B | `127.0.0.1` + correct pinned CA | TLS **fails** — no IP SAN on the leaf |
| C | `localhost` + wrong pinned CA | TLS **fails** — the pin is not vacuously accepting |

Measured on a live bring-up, 4/4. Two things that result establishes, neither of which CI can:

- **`PinnedCa` completes a real TLS handshake.** CI's real-broker rung runs a *plaintext*
  nats-server, and the unit tests call the validation callback directly with synthesized chains — so
  before this rig, the hand-built pinned path had production as its only exerciser.
- **A bogus credential is refused at connect**, against the real auth callout. That is §5's negative
  case, already confirmed on the desktop; if the player behaves differently, the difference is
  IL2CPP and nothing else.

A and C are the load-bearing pair: A alone would score identically against a validator that simply
returned `true`.

## 1. Stage the assemblies

```bash
sdks/unity/stage-sdk.sh
```

Expect **11** DLLs in `Runtime/Plugins/`, including `MQTTnet.dll`. MQTTnet has no transitive
package dependencies, so it arrives alone and needs no addition to the prune set.

Then apply the Unity 6 prune from [`io.devicechain.sdk/VERIFICATION.md`](io.devicechain.sdk/VERIFICATION.md)
§1 — delete the five facades Unity already ships, keep the other six.

> ⚠️ `VERIFICATION.md` §1 carries a ✅ from before MQTTnet was staged. If the Editor reports a
> duplicate assembly for `MQTTnet`, that checkmark was extended to a state no Editor had seen —
> record what you find rather than assuming the ✅ covered it.

## 2. Player settings that matter

- **Scripting backend: IL2CPP.** This is the whole point.
- **Managed stripping level: start at `Low`.** MQTTnet is reflection-free, so it should survive
  `High` — but if the round trip fails, drop the stripping level *first* and see whether it comes
  back. That single bisection distinguishes "IL2CPP cannot run this" from "the linker removed
  something", which are entirely different problems with entirely different fixes.
- **Api Compatibility Level: .NET Standard 2.1.**

## 3. The smallest scene that proves anything

One `GameObject`, one script. It publishes a measurement and answers one command — no site, no
machines, no terrain.

```csharp
// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk.Ingest;
using DeviceChain.Sdk.Mqtt;
using DeviceChain.Sdk.Transport;
using UnityEngine;

public sealed class MqttSmokeTest : MonoBehaviour
{
    // localhost, NOT 127.0.0.1 — the broker certificate has no IP SAN. See §0.
    [SerializeField] private string brokerUri = "ssl://localhost:1883";
    [SerializeField] private string instanceId = "devicechain";
    [SerializeField] private string tenant = "acme";
    [SerializeField] private string deviceToken = "sensor-001";
    [SerializeField] private string credentialId = "REPLACE-ME";

    // Drop nats-ca.pem into Assets/ renamed to `nats-ca.bytes` and drag it here. The `.bytes`
    // extension is what makes Unity import an arbitrary file as a TextAsset; a `.pem`/`.crt`
    // is not imported at all, so the field silently stays null.
    [SerializeField] private TextAsset pinnedCaPem;

    private MqttDeviceSession _session;
    private readonly CancellationTokenSource _cts = new();

    private async void Start()
    {
        // An unassigned TextAsset would otherwise surface as a NullReferenceException thrown out
        // of `async void`, where Unity may swallow it — a silent no-op is the worst outcome for a
        // page whose entire job is to distinguish "worked" from "did nothing".
        if (pinnedCaPem == null)
        {
            Debug.LogError("[dc] pinnedCaPem is not assigned — see §0 and §3");
            return;
        }

        var options = new MqttSessionOptions(
            new Uri(brokerUri), instanceId, tenant, deviceToken, credentialId)
        {
            // A local bring-up presents a privately-issued certificate, so the OS root store
            // cannot verify it — but pinning its CA still verifies the whole chain, and it is
            // the ONLY mode that exercises the hand-built validation under IL2CPP. Do not
            // substitute MqttTrust.DangerouslyAcceptAnyServerCertificate() here: it would make
            // the run pass while validating nothing, which is the exact false green this page
            // exists to avoid. (It is a legitimate BISECTION tool — see §6.)
            Trust = MqttTrust.PinnedCa(pinnedCaPem.bytes),
        };

        _session = new MqttDeviceSession(options);
        _session.StateChanged += s => Debug.Log($"[dc] session state: {s}");

        try
        {
            // Returns only after a CONFIRMED subscribe. If this throws
            // MqttSubscribeRefusedException the credential cannot read the command topic —
            // that is a provisioning answer, not an IL2CPP answer.
            await _session.StartAsync(OnCommandAsync, _cts.Token);
            Debug.Log("[dc] READY — connected and granted");

            var publisher = new DeviceEventPublisher(new MqttDeviceEventCarrier(_session, deviceToken));
            await publisher.EmitMeasurementsAsync(
                deviceToken, credentialId,
                new Dictionary<string, double> { ["fuelPct"] = 42.5 },
                cancellationToken: _cts.Token);
            Debug.Log("[dc] PUBLISHED a measurement (broker PUBACKed it)");
        }
        catch (Exception ex)
        {
            Debug.LogError($"[dc] FAILED: {ex.GetType().FullName}: {ex}");
        }
    }

    // 🔴 Runs on a BACKGROUND thread. The SDK deliberately does not marshal onto Unity's main
    // thread — doing that inside the SDK turns any throw into a silent hang. Touch no UnityEngine
    // API here beyond Debug.Log; queue real work to the main thread yourself.
    private Task<CommandOutcome> OnCommandAsync(DeviceCommand command, CancellationToken ct)
    {
        Debug.Log($"[dc] COMMAND {command.Name} ({command.Token})");

        // Answer for what the machine actually did. Returning Succeeded() here without acting is
        // the false-success this whole design exists to prevent — in a real scene this returns
        // only once the machine has accepted the task.
        return Task.FromResult(CommandOutcome.Succeeded("acknowledged by the player"));
    }

    private async void OnDestroy()
    {
        _cts.Cancel();
        if (_session != null)
        {
            await _session.DisposeAsync();
        }
    }
}
```

## 4. What to run, and what counts as a pass

Build a **native player** (not Play mode — Play mode is Mono and answers nothing). Run it, then:

1. **Publish** — the log shows `PUBLISHED`, and the measurement appears in event-management for
   that device.
2. **Capture stream** — the event is observable on the durability capture stream. This is the half
   HTTP ingress cannot do, and it is worth confirming explicitly rather than inferring from (1).
3. **Command** — issue a command to the device (console command widget, or `dcctl`). The log shows
   `COMMAND`, and the durable command reaches **`SUCCESSFUL`**.

All three, from the player, is the pass. Record the Unity version, platform, scripting backend and
stripping level alongside the result — a pass is a statement about that combination.

## 5. The negative case, which is the half that gets skipped

A gate is worth nothing until it has been shown to fail. Run **one** deliberately-wrong case:

> Change `credentialId` to a wrong value and run again.

The expected result is a **failure at startup**, not a session that connects and sits quiet:
`StartAsync` throws and the state reaches `Blind`. If instead the player logs `READY` and simply
never receives a command, the fail-closed subscribe has been lost somewhere between the SDK and
IL2CPP — and that is precisely the defect this lane was built to prevent, so it is the single most
valuable thing this page can catch.

## 5.5 The traps this page cost to find

Every one of these produced a run that *looked* like a failure and was not, or a change that looked
applied and was not. They are listed because each cost real time on 2026-08-08.

- 🔴 **An existing project's embedded package is probably STALE.** `DeviceChainTest`'s copy predated
  MQTT entirely — no `MQTTnet.dll` at all. Re-stage the **6** assemblies Unity 6 does not ship
  (`DeviceChain.Sdk`, `MQTTnet`, `Microsoft.Bcl.AsyncInterfaces`, `System.Text.Encodings.Web`,
  `System.Text.Json`, `System.Threading.Channels`) and never the 5 facades from the prune set. Check
  the assembly's *contents*, not its timestamp: `strings DeviceChain.Sdk.dll | grep -x MqttDeviceSession`.
- 🔴 **Unity 6 renamed Build Settings to `File → Build Profiles`** — and **your scene must be in the
  Scene List**. A build with only `SampleScene` enabled produces a player that starts, does nothing,
  and is indistinguishable from a failed connection.
- 🔴 **A `[SerializeField]` default cannot be changed from code.** The scene keeps the value saved
  with it, so editing the default is a silent no-op that reads as "the fix didn't work". The wait
  window is a `const` for exactly this reason.
- 🔴 **A fullscreen player gets closed mid-connect**, and the truncated log reads as a failure. The
  sample forces windowed and quits itself.
- 🔴 **Timing: a command is published 10–20s after enqueue**, on a delivery sweep that ticks about
  every 15s. Four runs missed because a 45s window closed seconds before the command arrived — none
  of them a defect. Give the player minutes, not seconds.
- 🔴 **`md5` of the DLL inside `_Data/Managed/` will NOT match the staged one** — Unity re-processes
  assemblies at build time. Compare behaviour, or hash what you stage rather than what ships.
- 🔴 **IL2CPP needs a real C++ toolchain, and "installed" is not the same as "usable".** VS 2022
  Community was present with **no `VC/Tools/MSVC` directory at all**, and `Windows Kits\10` held
  `Catalogs`/`Redist`/`UnionMetadata` but no `Include/` or `Lib/`. Install the **Desktop development
  with C++** workload — *not* "Game development with Unity", which is the tempting wrong answer and
  brings no compiler. Unity 6000.5.3f1 wants **VS 2022**; a VS 2026 install did not satisfy it.
  Verify all three paths before building:

  ```bash
  ls "/c/Program Files/Microsoft Visual Studio/2022/Community/VC/Tools/MSVC"   # e.g. 14.44.35207
  ls "/c/Program Files (x86)/Windows Kits/10/Include"                          # e.g. 10.0.26100.0
  ls "/c/Program Files (x86)/Windows Kits/10/Lib"                              # e.g. 10.0.26100.0
  ```

  Restart Unity afterwards — it probes for the toolchain at startup.

## 6. If it fails

Bisect in this order; each step separates causes that look identical from the log:

1. **Same binary, `MqttTrust.DangerouslyAcceptAnyServerCertificate()`.** Isolates *certificate
   validation* from TLS-and-MQTT: if it now works, the transport is fine and the problem is the
   pinned-chain code or the CA you supplied. Start here rather than with a plaintext broker — the
   bring-up terminates TLS on 1883 and offers **no plaintext listener**, so "try `tcp://`" is not a
   one-line change on a local cluster. **A pass in this mode is a diagnostic, never the result** —
   §4 is only satisfied under `PinnedCa`.
2. **Stripping level to `Low`** (if it was higher). Isolates the linker from IL2CPP.
3. **Mono player build.** If Mono works and IL2CPP does not, it is genuinely the AOT compiler —
   the case the whole lane was de-risked against, and the point at which the hand-rolled MQTT 3.1.1
   fallback becomes the answer. The transport seam (`IMqttConnection`) exists so that fallback is a
   drop-in: everything above it, including every test, survives the swap.
4. **`dotnet run` the same calls on the desktop.** If that works and the player does not, the
   difference is Unity's runtime, not the code.

Report the result either way. A failure here is a finding, not a setback — it is exactly what this
check was built to surface, and finding it against a one-cube scene is the cheapest it will ever be.
