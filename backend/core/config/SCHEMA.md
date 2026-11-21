<!-- Copyright The DeviceChain Authors -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Instance configuration schema

`instance-config.schema.json` is generated from `InstanceConfiguration` and its
reachable Go types, deterministic startup constraints, and `RetiredConfigKeys()`.
Do not edit the JSON by hand.
It describes the JSON document mounted at `/etc/dci-config/instance`, not Helm
values, an Instance CR, or the separate per-service config documents.

Every release attaches this file to its GitHub Release. Consumers must select
the **exact target tag**, including any prerelease suffix:

```text
https://github.com/devicechain-io/devicechain/releases/download/v1.9.0/instance-config.schema.json
```

The matching release's `dcctl config-schema` prints the same embedded bytes to
stdout, offline, without credentials or a running instance. Check `dcctl version`
to identify the binary; this command always describes its own build. Development
builds describe their checkout. There is deliberately no moving `latest` schema
or HTTP service endpoint: validation is needed before an instance exists, and
the existing release asset mechanism already provides version selection.

The dialect is JSON Schema 2020-12. Typed objects reject unknown keys at every
level. Properties use lower camel case for editor suggestions and also accept
the decoder's case-insensitive spelling. The document and `infrastructure` must
be objects; `infrastructure.nats` and `infrastructure.userManagement` must each
supply a nonempty `hostname` and a `port` in 1..65535. These endpoints have no
startup defaults. Other fields may be omitted; `null` follows `encoding/json`
and startup default behavior. Declared retired keys
are marked deprecated and remain accepted, since startup ignores their values
with a warning. Slices and map values retain their element types. Datastore
`configuration` maps intentionally accept arbitrary JSON: their driver owns
the nested contract.

The schema includes static enums (logging, tracing, messaging and token-audience
policy), port and duration bounds, and integer map-key ranges for the target Go
build. Empty strings, zeroes and nulls remain accepted where startup defaults
them. Successful schema validation is not a promise that startup succeeds:
`ApplyDefaults` and `Validate` still enforce cross-field rules (including drain
versus grace), certificates, credentials, URLs and deployment constraints.

**Integer token limitation:** JSON Schema validates numeric values, not their
original spelling. It accepts `4222.0` and `4.222e3` as integers; `encoding/json`
refuses those tokens for Go integer fields. Emit integer tokens without a decimal
point or exponent. Schema validation alone cannot check this representational
requirement; the startup loader remains authoritative.

Schema generation fails for unsupported codecs or shapes instead
of guessing a permissive schema. It never reads defaults, live configuration,
environment variables, credentials, or a cluster.

From `backend/core`:

```bash
go generate ./config
go run ./cmd/instance-config-schema -check config/instance-config.schema.json
```

The generator also supports stdout (no flags) and `-output <file>`. Check mode
never writes. `TestInstanceSchemaCommitted` runs the same byte comparison in the
normal core CI suite; its negative controls change a generated field name and
corrupt the artifact. Core parity tests compare generated-schema decisions with
decoding, defaults and validation; they pin the integer-token limitation too.
dcctl tests compile the artifact with a JSON Schema
validator, accept valid documents, reject unknown keys and incorrect types,
and verify the release asset declaration and command output.

The existing `wirecontract.Generate` targets authoring types with explicit JSON
tags and required fields. Instance decoding instead uses mostly untagged,
case-insensitive fields with startup defaults, accepts nulls, and has retired keys and
integer-keyed maps. Its generator therefore lives beside these config types
without changing the authoring contracts.
