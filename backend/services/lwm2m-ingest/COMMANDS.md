# LwM2M downlink commands (ADR-075 L4a + L4b)

The LwM2M ingest adapter dispatches platform-originated commands **down** to a connected LwM2M
device: a command created in `command-delivery` is consumed by the adapter (only on the serving
leader), mapped to a CoAP **Read / Write / Execute** on the device's live DTLS session, and its
outcome is reported back on `command-responses`.

This is the *first* true downlink adapter in the platform: an MQTT/NATS device subscribes to its own
command subject directly, but a CoAP device cannot, so the adapter stands in for it.

## The three command keys

Commands are **generic and path-addressed**: the profile declares three `CommandDefinition`s, and the
LwM2M object/instance/resource path (and, for a write, the value) rides in the command **payload**.
The adapter needs no per-device-type mapping.

| Command key      | CoAP op | Payload                                   | Success → `command-responses` |
|------------------|---------|-------------------------------------------|-------------------------------|
| `lwm2m.read`     | GET     | `{"path":"/3/0/9"}`                        | `success:true`, `payload` = the device's response body (text; base64 if opaque) |
| `lwm2m.write`    | PUT     | `{"path":"/5/0/1","value":<scalar>}`      | `success:true` |
| `lwm2m.execute`  | POST    | `{"path":"/5/0/2","args":"<optional>"}`   | `success:true` |

- **`path`** is an absolute LwM2M path with 1–4 numeric segments (`/objectId[/instanceId[/resourceId
  [/resourceInstanceId]]]`), each a 16-bit id. A malformed path is refused locally (the command
  reports `FAILED` without touching the wire).
- **`value`** (write) is a single JSON **scalar**: a string is written as-is, a number by its exact
  literal, a boolean as `1`/`0` (LwM2M text/plain). Object/array/null and multi-resource
  (SenML/TLV) writes are **not** supported in this slice.
- **`args`** (execute) is the optional LwM2M execute-argument string; omit it for a bare Execute.

A device's CoAP response class decides the outcome: `2.xx` → `SUCCESSFUL`; `4.xx`/`5.xx` → `FAILED`
with the code; no response within the timeout → `FAILED` (timeout). A `lwm2m.read` returns the
response body in the command's `responsePayload`.

## Queue-mode hold-and-drain (L4b)

A command for a device that is **connected right now** is dispatched immediately (the live path). A
command for a device that is **offline** — a queue-mode sleeper, or a device between sessions — is
**held**, and **drained to the device on its next wake**.

The hold is not new machinery: `command-delivery` already persists every command as a durable row and
leaves it in the `SENT` state after publishing. When a device next **Registers** or sends a
re-handshake **Update** (the LwM2M queue-mode wake signals), the serving leader queries
`command-delivery` for that device's still-`SENT` commands and dispatches them **oldest-first** over
the freshly live session — the same CoAP Read/Write/Execute mapping as the live path, on the same
per-device worker (so a drain never races or reorders the device's live commands).

A held command that is **never** delivered — the device never wakes before its horizon — reaches
**`TIMEOUT`**. Every command now carries a horizon: `command-delivery` stamps a **default TTL of 7
days** (aligned with the command-stream retention) on any command whose creator supplies no explicit
`expiresAt`, so a command can no longer sit in `SENT` forever. Tune it with
`command-delivery`'s `defaultCommandTtlSeconds`, or set a per-command `expiresAt` at enqueue.

Because a physical actuation firing twice is worse than a lost status report, the adapter **seals a
command's fate after the CoAP op runs**: if the device acted but the response could not be published
(a NATS blip), the command is *not* redelivered — it will `TIMEOUT` rather than re-actuate the device.
A command dispatched by the live path and a drain that fetches its still-`SENT` row are de-duplicated,
so the overlap never double-actuates a device.

**Boundaries (named):** the drain delivers a bounded batch per wake (the oldest ~32 held commands);
the rest drain on subsequent wakes. If the leader crashes *after* a drained op is issued but *before*
its response publishes, the command stays `SENT` and re-dispatches on the next wake — the platform's
documented at-least-once actuation posture, bounded by the horizon. A distinct `DELIVERED` state is
deliberately **not** reintroduced: CoAP is synchronous, so a Read/Write/Execute lands directly on
`SUCCESSFUL`/`FAILED`; "held for an offline device" is derivable from `SENT` + the device's presence.

## Firmware update over the air (Object 5) — a runbook

L4a does **not** add a firmware mechanism; FOTA is composed from the three primitives plus the
Firmware Update object (`/5`). Drive the steps **in order, waiting for each command to reach
`SUCCESSFUL` before issuing the next** — the adapter serializes a single device's commands, but the
firmware state machine itself requires ordering:

1. **Set the package URI** — write the image location to Firmware Package URI (`/5/0/1`):
   ```
   createCommand(name:"lwm2m.write", payload:{"path":"/5/0/1","value":"coaps://fw.example/image.bin"})
   ```
   The device begins downloading. (For inline delivery, `/5/0/0` Package is a large opaque write and
   is out of this slice's single-resource text/plain scope — prefer the URI method.)

2. **Trigger the update** — execute Firmware Update (`/5/0/2`) once the download has completed:
   ```
   createCommand(name:"lwm2m.execute", payload:{"path":"/5/0/2"})
   ```

3. **Watch progress / outcome** — read Firmware State (`/5/0/3`, `0`=idle … `3`=updating) and Update
   Result (`/5/0/5`, `0`=initial, `1`=success, `≥2`=an error):
   ```
   createCommand(name:"lwm2m.read", payload:{"path":"/5/0/3"})
   createCommand(name:"lwm2m.read", payload:{"path":"/5/0/5"})
   ```
   For **push** progress instead of polling, an operator may include Object 5 in the observe
   allowlist so `/5/0/3` and `/5/0/5` surface as numeric measurements (they are numeric enums); the
   GA-minimal path is read-on-demand.

## Interop

Validate against Eclipse **Leshan (pin the client to LwM2M 1.1)**: register a device, then a Read of a
resource, a Write of a resource, an Execute, and the FOTA sequence above. A conformant 1.0-only client
is served for presence and commands (Read sends no `Accept`, so it is not rejected), but its SenML
telemetry Observe is 4.06'd until the TLV decode follow-up.

## Tuning

`downlink.timeoutSeconds` (default 10) bounds one CoAP command exchange; raise it for slow cellular
radios. `downlink.concurrency` (default 16) sets cross-device dispatch parallelism; a single device's
commands always run in stream order regardless of the count.

---

*Follow-up: a Docusaurus `docs/` concept + command-reference page for LwM2M (mirroring
`concepts/sparkplug.md`) is a documentation task tracked separately from this backend slice.*
