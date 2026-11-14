<!-- Copyright The DeviceChain Authors -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

`dcctl backup inspect <backup-set-dir> --verify` reads `manifest.json` and
verifies all listed snapshots locally. Any validation, read, size, or hash error
returns a nonzero exit status, naming the affected entry. No cluster is contacted
and no files are written. `--verify` is required; `--verify=false` is rejected.

Version 1 manifest example:

```json
{
  "version": 1,
  "entries": [
    {
      "kind": "stream",
      "name": "capture",
      "backup_class": "checkpoint-required-if-nonempty",
      "snapshot": "streams/capture.snap",
      "size_bytes": 0,
      "sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
    }
  ]
}
```

All entry fields are required. Kinds are `stream` (including gateway streams),
`bucket` (KV), and `object-store` (an opaque export, not necessarily JetStream).
Classes use `durability.BackupClass` values: only `must-survive` and
`checkpoint-required-if-nonempty` preserve checkpoint content and are accepted.
Rebuildable, ephemeral, missing, and unknown classes are rejected for every kind.
No registry lookup overrides the manifest's declared policy.

Snapshots are distinct regular files at slash-separated paths under the backup
directory; absolute paths, traversal, Windows separators/drive prefixes, and
symlinks escaping the directory are rejected. Sizes are nonnegative integers;
SHA-256 digests are 64 lowercase hexadecimal characters. Resource identities
(`kind`, `name`) and snapshot paths must be unique. Empty inventories, unknown
versions/fields, duplicate JSON keys, malformed JSON, and trailing JSON values are rejected. Snapshots
are hashed as a stream, without loading archives into memory.

Success proves the listed bytes match the manifest. It does not authenticate the
manifest, inspect native archive internals, establish inventory completeness, or
prove a coordinated database/stream/blob recovery checkpoint. This slice defines
no backup writer, restore operation, COMPLETE marker, or cluster interface.
