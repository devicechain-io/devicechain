# MQTT device plane — native player verification

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
a command round trip. **Until the steps below have been run, the MQTT lane is CI-complete and
player-unverified.** Say it that way; do not round it up.

## The rule that governs this page

**Validate the transport against the smallest possible scene, never against the new one.** The first
player build that exercises MQTT should be the spinning-logo sample plus a single command receiver —
not the construction site. Debugging one unproven thing beats debugging two, and this rule is here
because the WebGL path was learned the expensive way.

---

## 0. Prerequisites

- A local DeviceChain cluster (`deploy/local/`), bootstrapped. On a local kube context `dcctl
  bootstrap` sets the MQTT node port automatically and the kind cluster maps host `1883`, so the
  broker answers at **`ssl://127.0.0.1:1883` with no port-forward**. Confirm before you build
  anything:

  ```bash
  # Should connect and stay open. If it refuses, the rest of this page cannot work.
  timeout 2 bash -c 'cat < /dev/null > /dev/tcp/127.0.0.1/1883' && echo "broker reachable"
  ```

- A provisioned device, and its **credential id**. The session needs `instanceId`, `tenant`,
  `deviceToken`, `credentialId` — nothing else.
- Unity 6 (the version `VERIFICATION.md` records) with **IL2CPP** and the platform module for your
  target. Mono will *not* answer the question this page exists to ask.

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
    [SerializeField] private string brokerUri = "ssl://127.0.0.1:1883";
    [SerializeField] private string instanceId = "devicechain";
    [SerializeField] private string tenant = "acme";
    [SerializeField] private string deviceToken = "sensor-001";
    [SerializeField] private string credentialId = "REPLACE-ME";

    private MqttDeviceSession _session;
    private readonly CancellationTokenSource _cts = new();

    private async void Start()
    {
        var options = new MqttSessionOptions(
            new Uri(brokerUri), instanceId, tenant, deviceToken, credentialId)
        {
            // A local bring-up presents a self-signed certificate. PinnedCa is the RIGHT answer
            // (it still verifies the chain); the accept-anything mode is named the way it is so
            // that shipping it by accident is impossible to describe as anything else.
            Trust = MqttTrust.DangerouslyAcceptAnyServerCertificate(),
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

## 6. If it fails

Bisect in this order; each step separates causes that look identical from the log:

1. **Same binary, `tcp://` against a plaintext broker.** Isolates TLS from MQTT.
2. **Stripping level to `Low`** (if it was higher). Isolates the linker from IL2CPP.
3. **Mono player build.** If Mono works and IL2CPP does not, it is genuinely the AOT compiler —
   the case the whole lane was de-risked against, and the point at which the hand-rolled MQTT 3.1.1
   fallback becomes the answer. The transport seam (`IMqttConnection`) exists so that fallback is a
   drop-in: everything above it, including every test, survives the swap.
4. **`dotnet run` the same calls on the desktop.** If that works and the player does not, the
   difference is Unity's runtime, not the code.

Report the result either way. A failure here is a finding, not a setback — it is exactly what this
check was built to surface, and finding it against a one-cube scene is the cheapest it will ever be.
