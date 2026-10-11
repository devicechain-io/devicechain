// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Helpers for an MQTT_BASIC device password. The platform stores only a keyed digest
// of it, so the operator is the one place it can be read, once, at the moment it is
// set. These keep that moment as safe as the browser allows.

// The server refuses a shorter password (counted in UTF-8 bytes). The console shows
// this as a hint and never blocks on it, so the server's own refusal stays the
// authority and is surfaced when it comes.
export const MIN_PASSWORD_BYTES = 16;

// Entropy of a generated password, in random bytes before encoding.
export const GENERATED_PASSWORD_BYTES = 24;

// The password's length as the server counts it: UTF-8 bytes, not characters. A
// password of ten emoji is 10 characters (20 UTF-16 units) and 40 bytes.
export function passwordByteLength(password: string): number {
  return new TextEncoder().encode(password).length;
}

// True when a non-empty password is under the minimum. Empty is "not entered yet",
// which is a different message.
export function passwordBelowMinimum(password: string): boolean {
  return password.length > 0 && passwordByteLength(password) < MIN_PASSWORD_BYTES;
}

// A fresh random password: GENERATED_PASSWORD_BYTES from the browser's CSPRNG,
// base64url without padding (32 characters of URL-safe ASCII, so characters equal
// bytes and nothing needs escaping in a device's configuration).
export function generatePassword(): string {
  const bytes = new Uint8Array(GENERATED_PASSWORD_BYTES);
  crypto.getRandomValues(bytes);
  let bin = '';
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}
