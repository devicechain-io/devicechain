// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the update-management service: the opt-in OTA area.
// It serves the per-tenant artifact catalogue, the signer registry (the public keys a
// tenant trusts to vouch for artifacts), signatures, and update assignments.
//
// This file carries EVERY operation the schema exposes so the pages built on it (the
// artifact catalogue, the signer registry, a device's Updates tab) share one generated
// client and never edit generated output. Pages must be gated on the area being deployed
// (see useAreaPresent) and on the authorities in lib/updateAuthority.ts; this module does
// neither, because a gate belongs to the surface, not to the transport.
//
// Artifact content travels on a separate streaming endpoint
// (PUT /updates/artifacts/{token}/content), not over GraphQL.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/update-management';
import type {
  UpdateManagementInfoQuery,
  ArtifactsQuery,
  ArtifactQuery,
  ArtifactSignersQuery,
  ArtifactSignerQuery,
  UpdateAssignmentsQuery,
  UpdateAssignmentQuery,
  ArtifactCreateRequest,
  ArtifactSearchCriteria,
  ArtifactSignerSearchCriteria,
  UpdateAssignmentSearchCriteria,
  ArtifactKind,
  ArtifactState,
  ArtifactTrustStatus,
  SignerAlgorithm,
  SignerState,
  UpdateState,
} from '@/gql/update-management/graphql';

export type UpdateManagementInfo = UpdateManagementInfoQuery['updateManagementInfo'];
export type Artifact = NonNullable<ArtifactQuery['artifact']>;
export type ArtifactSearchResults = ArtifactsQuery['artifacts'];
export type ArtifactSigner = NonNullable<ArtifactSignerQuery['artifactSigner']>;
export type ArtifactSignerSearchResults = ArtifactSignersQuery['artifactSigners'];
export type UpdateAssignment = NonNullable<UpdateAssignmentQuery['updateAssignment']>;
export type UpdateAssignmentSearchResults = UpdateAssignmentsQuery['updateAssignments'];
export type {
  ArtifactCreateRequest,
  ArtifactSearchCriteria,
  ArtifactSignerSearchCriteria,
  UpdateAssignmentSearchCriteria,
  ArtifactKind,
  ArtifactState,
  ArtifactTrustStatus,
  SignerAlgorithm,
  SignerState,
  UpdateState,
};

// ── Service info ────────────────────────────────────────────────────────

// maxArtifactBytes is a DECIMAL STRING because a byte count can exceed a 32-bit Int;
// parse it deliberately (BigInt), never let it coerce silently.
const UPDATE_MANAGEMENT_INFO = graphql(`
  query UpdateManagementInfo {
    updateManagementInfo {
      area
      allowUnverifiedArtifacts
      maxArtifactBytes
    }
  }
`);

// ── Artifacts ───────────────────────────────────────────────────────────

const ARTIFACTS = graphql(`
  query Artifacts($criteria: ArtifactSearchCriteria!) {
    artifacts(criteria: $criteria) {
      results {
        id
        createdAt
        updatedAt
        token
        kind
        name
        version
        component
        sizeBytes
        digest
        mediaType
        compatibleProfiles
        trustStatus
        provenance
        state
      }
      pagination {
        pageStart
        pageEnd
        totalRecords
      }
    }
  }
`);

const ARTIFACT = graphql(`
  query Artifact($token: String!) {
    artifact(token: $token) {
      id
      createdAt
      updatedAt
      token
      kind
      name
      version
      component
      sizeBytes
      digest
      mediaType
      compatibleProfiles
      trustStatus
      provenance
      state
    }
  }
`);

const CREATE_ARTIFACT = graphql(`
  mutation CreateArtifact($request: ArtifactCreateRequest!) {
    createArtifact(request: $request) {
      id
      createdAt
      updatedAt
      token
      kind
      name
      version
      component
      sizeBytes
      digest
      mediaType
      compatibleProfiles
      trustStatus
      provenance
      state
    }
  }
`);

const RETIRE_ARTIFACT = graphql(`
  mutation RetireArtifact($token: String!) {
    retireArtifact(token: $token) {
      id
      createdAt
      updatedAt
      token
      kind
      name
      version
      component
      sizeBytes
      digest
      mediaType
      compatibleProfiles
      trustStatus
      provenance
      state
    }
  }
`);

// Returns the artifact with its RECOMPUTED trustStatus, so a caller can show the new
// trust without a second read.
const ADD_ARTIFACT_SIGNATURE = graphql(`
  mutation AddArtifactSignature($artifactToken: String!, $signerToken: String!, $signature: String!) {
    addArtifactSignature(artifactToken: $artifactToken, signerToken: $signerToken, signature: $signature) {
      id
      createdAt
      updatedAt
      token
      kind
      name
      version
      component
      sizeBytes
      digest
      mediaType
      compatibleProfiles
      trustStatus
      provenance
      state
    }
  }
`);

// ── Signers ─────────────────────────────────────────────────────────────

const ARTIFACT_SIGNERS = graphql(`
  query ArtifactSigners($criteria: ArtifactSignerSearchCriteria!) {
    artifactSigners(criteria: $criteria) {
      results {
        id
        createdAt
        token
        name
        algorithm
        publicKeyPem
        keyId
        state
        revokedAt
        createdBy
        revokedBy
        soleSignedArtifactCount
      }
      pagination {
        pageStart
        pageEnd
        totalRecords
      }
    }
  }
`);

const ARTIFACT_SIGNER = graphql(`
  query ArtifactSigner($token: String!) {
    artifactSigner(token: $token) {
      id
      createdAt
      token
      name
      algorithm
      publicKeyPem
      keyId
      state
      revokedAt
      createdBy
      revokedBy
      soleSignedArtifactCount
    }
  }
`);

const REGISTER_ARTIFACT_SIGNER = graphql(`
  mutation RegisterArtifactSigner($name: String!, $publicKeyPem: String!) {
    registerArtifactSigner(name: $name, publicKeyPem: $publicKeyPem) {
      id
      createdAt
      token
      name
      algorithm
      publicKeyPem
      keyId
      state
      revokedAt
      createdBy
      revokedBy
      soleSignedArtifactCount
    }
  }
`);

const REVOKE_ARTIFACT_SIGNER = graphql(`
  mutation RevokeArtifactSigner($token: String!) {
    revokeArtifactSigner(token: $token) {
      id
      createdAt
      token
      name
      algorithm
      publicKeyPem
      keyId
      state
      revokedAt
      createdBy
      revokedBy
      soleSignedArtifactCount
    }
  }
`);

// ── Update assignments ──────────────────────────────────────────────────

const UPDATE_ASSIGNMENTS = graphql(`
  query UpdateAssignments($criteria: UpdateAssignmentSearchCriteria!) {
    updateAssignments(criteria: $criteria) {
      results {
        id
        createdAt
        token
        deviceToken
        component
        artifact {
          id
          createdAt
          updatedAt
          token
          kind
          name
          version
          component
          sizeBytes
          digest
          mediaType
          compatibleProfiles
          trustStatus
          provenance
          state
        }
        actor
        supersededAt
        current
        attempt {
          token
          state
          targetVersion
          targetDigest
          requiresReboot
          lastReportAt
          progress {
            bytes
            total
          }
          failure {
            stage
            code
            detail
            platform
          }
          cancelRequested
          reconciled
        }
        signedBy {
          token
          name
          keyId
          algorithm
          state
        }
      }
      pagination {
        pageStart
        pageEnd
        totalRecords
      }
    }
  }
`);

const UPDATE_ASSIGNMENT = graphql(`
  query UpdateAssignment($token: String!) {
    updateAssignment(token: $token) {
      id
      createdAt
      token
      deviceToken
      component
      artifact {
        id
        createdAt
        updatedAt
        token
        kind
        name
        version
        component
        sizeBytes
        digest
        mediaType
        compatibleProfiles
        trustStatus
        provenance
        state
      }
      actor
      supersededAt
      current
      attempt {
        token
        state
        targetVersion
        targetDigest
        requiresReboot
        lastReportAt
        progress {
          bytes
          total
        }
        failure {
          stage
          code
          detail
          platform
        }
        cancelRequested
        reconciled
      }
      signedBy {
        token
        name
        keyId
        algorithm
        state
      }
    }
  }
`);

const ASSIGN_ARTIFACT = graphql(`
  mutation AssignArtifact($deviceToken: String!, $artifactToken: String!) {
    assignArtifact(deviceToken: $deviceToken, artifactToken: $artifactToken) {
      id
      createdAt
      token
      deviceToken
      component
      artifact {
        id
        createdAt
        updatedAt
        token
        kind
        name
        version
        component
        sizeBytes
        digest
        mediaType
        compatibleProfiles
        trustStatus
        provenance
        state
      }
      actor
      supersededAt
      current
      attempt {
        token
        state
        targetVersion
        targetDigest
        requiresReboot
        lastReportAt
        progress {
          bytes
          total
        }
        failure {
          stage
          code
          detail
          platform
        }
        cancelRequested
        reconciled
      }
      signedBy {
        token
        name
        keyId
        algorithm
        state
      }
    }
  }
`);

const CANCEL_UPDATE = graphql(`
  mutation CancelUpdate($assignmentToken: String!) {
    cancelUpdate(assignmentToken: $assignmentToken) {
      id
      createdAt
      token
      deviceToken
      component
      artifact {
        id
        createdAt
        updatedAt
        token
        kind
        name
        version
        component
        sizeBytes
        digest
        mediaType
        compatibleProfiles
        trustStatus
        provenance
        state
      }
      actor
      supersededAt
      current
      attempt {
        token
        state
        targetVersion
        targetDigest
        requiresReboot
        lastReportAt
        progress {
          bytes
          total
        }
        failure {
          stage
          code
          detail
          platform
        }
        cancelRequested
        reconciled
      }
      signedBy {
        token
        name
        keyId
        algorithm
        state
      }
    }
  }
`);

const ABANDON_UNKNOWN_UPDATE = graphql(`
  mutation AbandonUnknownUpdate($assignmentToken: String!, $reason: String!) {
    abandonUnknownUpdate(assignmentToken: $assignmentToken, reason: $reason) {
      id
      createdAt
      token
      deviceToken
      component
      artifact {
        id
        createdAt
        updatedAt
        token
        kind
        name
        version
        component
        sizeBytes
        digest
        mediaType
        compatibleProfiles
        trustStatus
        provenance
        state
      }
      actor
      supersededAt
      current
      attempt {
        token
        state
        targetVersion
        targetDigest
        requiresReboot
        lastReportAt
        progress {
          bytes
          total
        }
        failure {
          stage
          code
          detail
          platform
        }
        cancelRequested
        reconciled
      }
      signedBy {
        token
        name
        keyId
        algorithm
        state
      }
    }
  }
`);

// ── Client functions ────────────────────────────────────────────────────

export async function getUpdateManagementInfo(): Promise<UpdateManagementInfo> {
  const data = await gql('update-management', UPDATE_MANAGEMENT_INFO);
  return data.updateManagementInfo;
}

export async function listArtifacts(criteria: ArtifactSearchCriteria): Promise<ArtifactSearchResults> {
  const data = await gql('update-management', ARTIFACTS, { criteria });
  return data.artifacts;
}

export async function getArtifact(token: string): Promise<Artifact | null> {
  const data = await gql('update-management', ARTIFACT, { token });
  return data.artifact ?? null;
}

export async function createArtifact(request: ArtifactCreateRequest): Promise<Artifact> {
  const data = await gql('update-management', CREATE_ARTIFACT, { request });
  return data.createArtifact;
}

export async function retireArtifact(token: string): Promise<Artifact> {
  const data = await gql('update-management', RETIRE_ARTIFACT, { token });
  return data.retireArtifact;
}

export async function addArtifactSignature(opts: {
  artifactToken: string;
  signerToken: string;
  signature: string;
}): Promise<Artifact> {
  const data = await gql('update-management', ADD_ARTIFACT_SIGNATURE, opts);
  return data.addArtifactSignature;
}

export async function listArtifactSigners(
  criteria: ArtifactSignerSearchCriteria,
): Promise<ArtifactSignerSearchResults> {
  const data = await gql('update-management', ARTIFACT_SIGNERS, { criteria });
  return data.artifactSigners;
}

export async function getArtifactSigner(token: string): Promise<ArtifactSigner | null> {
  const data = await gql('update-management', ARTIFACT_SIGNER, { token });
  return data.artifactSigner ?? null;
}

export async function registerArtifactSigner(opts: {
  name: string;
  publicKeyPem: string;
}): Promise<ArtifactSigner> {
  const data = await gql('update-management', REGISTER_ARTIFACT_SIGNER, opts);
  return data.registerArtifactSigner;
}

export async function revokeArtifactSigner(token: string): Promise<ArtifactSigner> {
  const data = await gql('update-management', REVOKE_ARTIFACT_SIGNER, { token });
  return data.revokeArtifactSigner;
}

export async function listUpdateAssignments(
  criteria: UpdateAssignmentSearchCriteria,
): Promise<UpdateAssignmentSearchResults> {
  const data = await gql('update-management', UPDATE_ASSIGNMENTS, { criteria });
  return data.updateAssignments;
}

export async function getUpdateAssignment(token: string): Promise<UpdateAssignment | null> {
  const data = await gql('update-management', UPDATE_ASSIGNMENT, { token });
  return data.updateAssignment ?? null;
}

export async function assignArtifact(opts: {
  deviceToken: string;
  artifactToken: string;
}): Promise<UpdateAssignment> {
  const data = await gql('update-management', ASSIGN_ARTIFACT, opts);
  return data.assignArtifact;
}

export async function cancelUpdate(assignmentToken: string): Promise<UpdateAssignment> {
  const data = await gql('update-management', CANCEL_UPDATE, { assignmentToken });
  return data.cancelUpdate;
}

export async function abandonUnknownUpdate(opts: {
  assignmentToken: string;
  reason: string;
}): Promise<UpdateAssignment> {
  const data = await gql('update-management', ABANDON_UNKNOWN_UPDATE, opts);
  return data.abandonUnknownUpdate;
}
