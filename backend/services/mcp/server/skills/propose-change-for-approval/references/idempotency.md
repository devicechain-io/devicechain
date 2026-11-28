# Request keys

Every proposal carries a `requestId` that you choose. The server forwards it unchanged and never makes one up.

- 1 to 128 printable characters.
- Make a new key for each submission you mean as a new request, for example from what it is for and the time: `ack-incident-3-20261127T1042Z`.
- Reuse a key, unchanged, only to retry the same submission after a failure or a timeout. The retry must carry the same arguments.
- A retry with the same key and the same request returns the original proposal with `status` `EXISTING`. Nothing is stored twice.
- The same key with a different request fails with `CONFLICT`. Make a new key; do not edit the old request to fit.
- A dry run stores nothing. Use a separate key for the real submission.
- The platform remembers keys for 30 days after a proposal is decided.

For a command, `expiresAt` is fixed at the first submission. A retry must send the same value or leave it out the same way.
