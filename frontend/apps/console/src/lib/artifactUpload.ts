// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Artifact content upload: the one operation of the update-management area that does not
// travel over GraphQL. The bytes go to a streaming endpoint that authenticates before it
// reads the body and then verifies the size and SHA-256 against what was declared.
//
// It uses XMLHttpRequest rather than fetch for one reason: fetch exposes no UPLOAD progress,
// and a firmware image is large enough that a progress bar is the difference between
// "working" and "hung". Authentication is the console's own — the same access token every
// GraphQL call and the branding-logo upload attach (resolveAuthToken).
import { resolveAuthToken } from '@devicechain/client';

/** What the content endpoint answers on success: the artifact is now AVAILABLE. */
export interface ArtifactUploadResult {
  token: string;
  state: string;
  sizeBytes: string;
  digest: string;
}

/** A refused upload. `message` is the server's own body, verbatim. */
export class ArtifactUploadError extends Error {
  readonly status: number;
  constructor(message: string, status: number) {
    super(message);
    this.name = 'ArtifactUploadError';
    this.status = status;
  }
}

/** The content endpoint for an artifact, relative to the console's origin. */
export function artifactContentUrl(token: string): string {
  return `/api/update-management/updates/artifacts/${encodeURIComponent(token)}/content`;
}

/**
 * Parse the service's maxArtifactBytes (a decimal string, because a byte count can exceed
 * 32 bits). Returns null when it is not a plain non-negative integer, and the caller must
 * treat that as "limit unknown", never as "no limit".
 */
export function parseByteCount(raw: string | null | undefined): bigint | null {
  if (raw == null || !/^\d+$/.test(raw)) return null;
  return BigInt(raw);
}

/** Human-readable size (binary units). Exact bytes stay available in a title. */
export function formatBytes(raw: string | bigint | number): string {
  const n = typeof raw === 'bigint' ? raw : parseByteCount(String(raw));
  if (n == null) return String(raw);
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let v = Number(n);
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return i === 0 ? `${v} B` : `${v.toFixed(v >= 100 ? 0 : 1)} ${units[i]}`;
}

/**
 * The artifact digest form the service declares: `sha256:` + 64 lowercase hex digits.
 *
 * Known limit: WebCrypto has no incremental digest, so the whole file is read into ONE
 * ArrayBuffer. The hash itself runs off the main thread, but memory is the file's size, so
 * a file near a raised maxArtifactBytes (the default is 512 MiB) can fail to allocate. That
 * failure surfaces as an error to the caller, never as a digest. A chunked hash is the fix
 * if instances start carrying multi-GB images.
 */
export async function sha256Digest(file: Blob): Promise<string> {
  const buf = await file.arrayBuffer();
  const hash = await crypto.subtle.digest('SHA-256', buf);
  const hex = Array.from(new Uint8Array(hash))
    .map((b) => b.toString(16).padStart(2, '0'))
    .join('');
  return `sha256:${hex}`;
}

export interface UploadOptions {
  onProgress?: (loaded: number, total: number) => void;
  signal?: AbortSignal;
}

/**
 * PUT the file to the artifact's content endpoint. Resolves with the service's answer, or
 * rejects with an ArtifactUploadError carrying the server's refusal text verbatim (or a
 * transport message when nothing answered).
 */
export async function uploadArtifactContent(
  token: string,
  file: File,
  { onProgress, signal }: UploadOptions = {},
): Promise<ArtifactUploadResult> {
  const bearer = await resolveAuthToken();
  // Fail closed: an unauthenticated PUT would only be refused after a wasted request, and
  // the refusal ("missing bearer token") says nothing the console can act on.
  if (!bearer) throw new ArtifactUploadError('Not signed in.', 401);
  return new Promise<ArtifactUploadResult>((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open('PUT', artifactContentUrl(token));
    xhr.setRequestHeader('Authorization', `Bearer ${bearer}`);
    xhr.setRequestHeader('Content-Type', file.type || 'application/octet-stream');
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable) onProgress?.(e.loaded, e.total);
    };
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) {
        try {
          resolve(JSON.parse(xhr.responseText) as ArtifactUploadResult);
        } catch {
          // A 2xx we cannot read is NOT a success to report: the verification result is
          // the point of the call.
          reject(new ArtifactUploadError('The server answered but its reply could not be read.', xhr.status));
        }
        return;
      }
      reject(new ArtifactUploadError(xhr.responseText.trim() || `Upload failed (${xhr.status})`, xhr.status));
    };
    xhr.onerror = () => reject(new ArtifactUploadError('Could not reach the server.', 0));
    xhr.ontimeout = () => reject(new ArtifactUploadError('The upload timed out.', 0));
    xhr.onabort = () => reject(new ArtifactUploadError('The upload was cancelled.', 0));
    signal?.addEventListener('abort', () => xhr.abort(), { once: true });
    xhr.send(file);
  });
}
