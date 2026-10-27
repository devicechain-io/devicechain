# Sparkplug command downlink - implementation notes

Maintainer notes for the command path in `host/` (birthtable.go, commandenc.go). User-facing
documentation lives with the docs site.

- A command is addressed and encoded against the BIRTH metric table: alias only when the BIRTH
  aliased the metric, name otherwise (and always for Rebirth); the value is encoded exactly into
  the BIRTH-declared datatype or refused before the wire.
- A lookup answers `ErrNotBorn` when there is no usable table yet (node or device not born, the
  device not re-declared since a rebirth, or the Host session ended) - park and wait - and
  `ErrMetricUnknown` only when the table exists and does not declare the metric - refuse.
- **The revalidate-to-publish window is irreducible.** `RevalidateMetric` is checked under the
  tracker lock, the publish is not (an MQTT publish is never made while holding it), so a rebirth
  can land in between and the command goes out on an alias the node has just remapped. The
  confirmation stage settles this honestly: a write the node did not act on ends UNANSWERED.
