/* eslint-disable */
/** Internal type. DO NOT USE DIRECTLY. */
type Exact<T extends { [key: string]: unknown }> = { [K in keyof T]: T[K] };
/** Internal type. DO NOT USE DIRECTLY. */
export type Incremental<T> = T | { [P in keyof T]?: P extends ' $fragmentName' | '__typename' ? T[P] : never };
import { DocumentTypeDecoration } from '@graphql-typed-document-node/core';
/** The data needed to declare an artifact. No content travels with it. */
export type ArtifactCreateRequest = {
  /** Tokens of the device profiles the artifact may be assigned to. At least one; each must exist. */
  compatibleProfiles: Array<string>;
  /** The part of the device the artifact updates, for example firmware or app/agent. */
  component: string;
  /** The content's SHA-256 digest, as sha256: followed by 64 hex digits. An upload that hashes differently is refused. */
  digest: string;
  /** What the artifact is. */
  kind: ArtifactKind;
  /** The IANA media type of the content. Defaults to application/octet-stream. */
  mediaType?: string | null | undefined;
  /** The artifact's name. */
  name: string;
  /** Free-form notes, recorded as provenance. */
  notes?: string | null | undefined;
  /**
   * The exact size of the content in bytes, as a decimal string. An upload that delivers any
   * other number of bytes is refused.
   */
  sizeBytes: string;
  /** Where the artifact came from, recorded as provenance. */
  source?: string | null | undefined;
  /** The artifact's token, unique within the tenant. */
  token: string;
  /** The artifact's version label. Name, component and version together must be unique within the tenant. */
  version: string;
};

/** What an update artifact is. */
export type ArtifactKind =
  /** A configuration bundle installed as an update. */
  | 'CONFIGURATION'
  /** A device firmware image. */
  | 'FIRMWARE'
  /** An application or agent software package. */
  | 'SOFTWARE';

/** Criteria for searching artifacts. Every filter is optional. */
export type ArtifactSearchCriteria = {
  /** Only artifacts for this component. */
  component?: string | null | undefined;
  /** Only artifacts of this kind. */
  kind?: ArtifactKind | null | undefined;
  /** Page to return, starting at 1. A value below 1 is treated as 1. */
  pageNumber: number;
  /** Records per page. */
  pageSize: number;
  /** Only artifacts in this state. */
  state?: ArtifactState | null | undefined;
  /** Only artifacts with this derived trust. */
  trustStatus?: ArtifactTrustStatus | null | undefined;
};

/** Criteria for searching signers. Every filter is optional. */
export type ArtifactSignerSearchCriteria = {
  /** Page to return, starting at 1. A value below 1 is treated as 1. */
  pageNumber: number;
  /** Records per page. */
  pageSize: number;
  /** Only signers in this state. */
  state?: SignerState | null | undefined;
};

/** Where an artifact is in its lifecycle. */
export type ArtifactState =
  /** The content is stored and matches the declared size and digest. The artifact is immutable. */
  | 'AVAILABLE'
  /** Withdrawn from new use. Its metadata and content remain. */
  | 'RETIRED'
  /** Declared, with no verified content yet. The only state in which content can be uploaded. */
  | 'UPLOADING';

/**
 * How far an artifact's origin has been established. It is DERIVED each time it is read from the
 * signatures on record and the state of the signers behind them, so revoking a signer changes what
 * every artifact it vouched for reports, without any write to the artifact.
 */
export type ArtifactTrustStatus =
  /** At least one signature by an ACTIVE signer verified against the artifact's digest. */
  | 'SIGNED'
  /** No signature by an ACTIVE signer: the platform checks that the bytes are the declared bytes and does not attribute them to anyone. */
  | 'UNVERIFIED';

/** The algorithm of a signer's key. */
export type SignerAlgorithm =
  /** ECDSA over the NIST P-256 curve, signing the SHA-256 digest (ASN.1 DER signature). */
  | 'ECDSA_P256'
  /** Ed25519, signing the raw 32-byte SHA-256 digest. */
  | 'ED25519';

/** Whether a signer's signatures still count. */
export type SignerState =
  /** The signer's signatures count toward an artifact's trust. */
  | 'ACTIVE'
  /** Terminal. The signer's signatures stay on record and no longer count. */
  | 'REVOKED';

/** Criteria for searching update assignments. Every filter is optional. */
export type UpdateAssignmentSearchCriteria = {
  /** Only this component's assignments. */
  component?: string | null | undefined;
  /** Only each device's current assignment, not the superseded history. */
  currentOnly?: boolean | null | undefined;
  /** Only this device's assignments. */
  deviceToken?: string | null | undefined;
  /** Page to return, starting at 1. A value below 1 is treated as 1. */
  pageNumber: number;
  /** Records per page. */
  pageSize: number;
};

/** A rollout to declare. It is created DRAFT and does nothing until it is started. */
export type UpdateRolloutCreateRequest = {
  /** The AVAILABLE artifact to roll out. */
  artifactToken: string;
  /** Pause the rollout once this many devices failed or have an unknown outcome. Omit it to never pause on failures. */
  failureThreshold?: number | null | undefined;
  /** The device group whose members, as of the start, are the targets. */
  groupToken: string;
  /** The most devices whose update may be unfinished at once, from 1 to 1000. */
  maxInFlight: number;
  /** A label; the token when omitted. */
  name?: string | null | undefined;
  /** The rollout's token, unique in the tenant. */
  token: string;
};

/** Where one device of a rollout stands. Every outcome after dispatch is read from the device's own update attempt. */
export type UpdateRolloutOutcome =
  /** The rollout was aborted before this device was dispatched, or its update was cancelled. */
  | 'CANCELLED'
  /** The device's update attempt FAILED or TIMED_OUT. */
  | 'FAILED'
  /** Dispatched; the device's update attempt has not finished. */
  | 'IN_FLIGHT'
  /** Not dispatched yet. */
  | 'PENDING'
  /** The device was refused at dispatch (skipCode says why) and was not told anything. */
  | 'SKIPPED'
  /** The device confirmed it runs the artifact (UPDATED). */
  | 'SUCCEEDED'
  /** The device's update outcome is UNKNOWN; it holds its slot and counts toward the failure threshold. */
  | 'UNKNOWN';

/** Why a rollout is paused. */
export type UpdateRolloutPauseReason =
  /** The artifact can no longer be assigned: it was retired, or it lost the signature this instance requires. */
  | 'ARTIFACT_NOT_ASSIGNABLE'
  /** The number of failed or unknown devices reached the rollout's failure threshold. */
  | 'FAILURE_THRESHOLD'
  /** An operator paused it. */
  | 'OPERATOR';

/** Criteria for searching rollouts. */
export type UpdateRolloutSearchCriteria = {
  /** Page to return, starting at 1. A value below 1 is treated as 1. */
  pageNumber: number;
  /** Records per page. */
  pageSize: number;
  /** Only rollouts in this state. */
  state?: UpdateRolloutState | null | undefined;
};

/** Where a group rollout is in its lifecycle. */
export type UpdateRolloutState =
  /** Ended by an operator. Devices not yet dispatched were cancelled; devices already working on the update were left to finish. */
  | 'ABORTED'
  /** Every target was dispatched or skipped and no device is still working on the update. */
  | 'COMPLETED'
  /** Declared; no device has been resolved or told anything. */
  | 'DRAFT'
  /** No further device is dispatched until the rollout is resumed. Devices already working on the update carry on. */
  | 'PAUSED'
  /** The group's devices were frozen as the target set and are being dispatched, at most maxInFlight at a time. */
  | 'RUNNING';

/** Criteria for paging a rollout's targets. */
export type UpdateRolloutTargetSearchCriteria = {
  /** Only targets with this outcome. */
  outcome?: UpdateRolloutOutcome | null | undefined;
  /** Page to return, starting at 1. A value below 1 is treated as 1. */
  pageNumber: number;
  /** Records per page. */
  pageSize: number;
};

/** Where one update attempt is. The platform maps what the device reports to these states. */
export type UpdateState =
  /** The update was cancelled before installation began. */
  | 'CANCELLED'
  /** The device holds all the bytes. */
  | 'DOWNLOADED'
  /** The device is downloading the artifact. */
  | 'DOWNLOADING'
  /** The device reported a failure, or came back on the wrong version. */
  | 'FAILED'
  /** The device acknowledged the assignment. */
  | 'INITIATED'
  /** The platform created the attempt and the device has not reported. */
  | 'QUEUED'
  /** The device is rebooting into the new version. */
  | 'REBOOTING'
  /** A phase deadline passed before installation was reported started. */
  | 'TIMED_OUT'
  /** A phase deadline passed during installation or reboot. The attempt waits for evidence and is neither failed nor succeeded. */
  | 'UNKNOWN'
  /** The device is running the target version. Reached only on confirmation evidence: the version, and a new boot when the artifact needs one. */
  | 'UPDATED'
  /** The device is installing. */
  | 'UPDATING'
  /** The bytes the device holds hash to the artifact's digest. This is integrity only: it does not say who produced them. */
  | 'VERIFIED';

export type DeviceUpdateAssignmentsQueryVariables = Exact<{
  criteria: UpdateAssignmentSearchCriteria;
}>;


export type DeviceUpdateAssignmentsQuery = { updateAssignments: { results: Array<{ id: string, createdAt: string | null, token: string, deviceToken: string, component: string, actor: string | null, supersededAt: string | null, current: boolean, artifact: { id: string, token: string, kind: ArtifactKind, name: string, version: string, component: string, trustStatus: ArtifactTrustStatus, state: ArtifactState }, attempt: { token: string, state: UpdateState, targetVersion: string, requiresReboot: boolean, lastReportAt: string | null, cancelRequested: boolean, reconciled: boolean, progress: { bytes: string, total: string } | null, failure: { stage: string, code: string, detail: string, platform: boolean } | null } }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type DeviceUpdateAssignmentsScopedQueryVariables = Exact<{
  criteria: UpdateAssignmentSearchCriteria;
}>;


export type DeviceUpdateAssignmentsScopedQuery = { updateAssignments: { results: Array<{ id: string, createdAt: string | null, token: string, deviceToken: string, component: string, supersededAt: string | null, current: boolean, artifact: { id: string, token: string, kind: ArtifactKind, name: string, version: string, component: string, trustStatus: ArtifactTrustStatus, state: ArtifactState }, attempt: { token: string, state: UpdateState, targetVersion: string, requiresReboot: boolean, lastReportAt: string | null, cancelRequested: boolean, reconciled: boolean, progress: { bytes: string, total: string } | null, failure: { stage: string, code: string, detail: string, platform: boolean } | null } }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type AssignmentSignerQueryVariables = Exact<{
  token: string;
}>;


export type AssignmentSignerQuery = { updateAssignment: { token: string, signedBy: { token: string, name: string, keyId: string, algorithm: SignerAlgorithm, state: SignerState } | null } | null };

export type UpdateManagementInfoQueryVariables = Exact<{ [key: string]: never; }>;


export type UpdateManagementInfoQuery = { updateManagementInfo: { area: string, allowUnverifiedArtifacts: boolean, maxArtifactBytes: string, downloadsConfigured: boolean, objectStoreConfigured: boolean } };

export type ArtifactsQueryVariables = Exact<{
  criteria: ArtifactSearchCriteria;
}>;


export type ArtifactsQuery = { artifacts: { results: Array<{ id: string, createdAt: string | null, updatedAt: string | null, token: string, kind: ArtifactKind, name: string, version: string, component: string, sizeBytes: string, digest: string, mediaType: string, compatibleProfiles: Array<string>, trustStatus: ArtifactTrustStatus, provenance: string | null, state: ArtifactState }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type ArtifactQueryVariables = Exact<{
  token: string;
}>;


export type ArtifactQuery = { artifact: { id: string, createdAt: string | null, updatedAt: string | null, token: string, kind: ArtifactKind, name: string, version: string, component: string, sizeBytes: string, digest: string, mediaType: string, compatibleProfiles: Array<string>, trustStatus: ArtifactTrustStatus, provenance: string | null, state: ArtifactState, signatures: Array<{ id: string, createdAt: string | null, createdBy: string | null, countsTowardTrust: boolean, signer: { id: string, token: string, name: string, keyId: string, algorithm: SignerAlgorithm, state: SignerState } }> } | null };

export type CreateArtifactMutationVariables = Exact<{
  request: ArtifactCreateRequest;
}>;


export type CreateArtifactMutation = { createArtifact: { id: string, createdAt: string | null, updatedAt: string | null, token: string, kind: ArtifactKind, name: string, version: string, component: string, sizeBytes: string, digest: string, mediaType: string, compatibleProfiles: Array<string>, trustStatus: ArtifactTrustStatus, provenance: string | null, state: ArtifactState } };

export type RetireArtifactMutationVariables = Exact<{
  token: string;
}>;


export type RetireArtifactMutation = { retireArtifact: { id: string, createdAt: string | null, updatedAt: string | null, token: string, kind: ArtifactKind, name: string, version: string, component: string, sizeBytes: string, digest: string, mediaType: string, compatibleProfiles: Array<string>, trustStatus: ArtifactTrustStatus, provenance: string | null, state: ArtifactState } };

export type AddArtifactSignatureMutationVariables = Exact<{
  artifactToken: string;
  signerToken: string;
  signature: string;
}>;


export type AddArtifactSignatureMutation = { addArtifactSignature: { id: string, createdAt: string | null, updatedAt: string | null, token: string, kind: ArtifactKind, name: string, version: string, component: string, sizeBytes: string, digest: string, mediaType: string, compatibleProfiles: Array<string>, trustStatus: ArtifactTrustStatus, provenance: string | null, state: ArtifactState } };

export type ArtifactSignersQueryVariables = Exact<{
  criteria: ArtifactSignerSearchCriteria;
}>;


export type ArtifactSignersQuery = { artifactSigners: { results: Array<{ id: string, createdAt: string | null, token: string, name: string, algorithm: SignerAlgorithm, publicKeyPem: string, keyId: string, state: SignerState, revokedAt: string | null, createdBy: string | null, revokedBy: string | null, soleSignedArtifactCount: number }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type ArtifactSignerQueryVariables = Exact<{
  token: string;
}>;


export type ArtifactSignerQuery = { artifactSigner: { id: string, createdAt: string | null, token: string, name: string, algorithm: SignerAlgorithm, publicKeyPem: string, keyId: string, state: SignerState, revokedAt: string | null, createdBy: string | null, revokedBy: string | null, soleSignedArtifactCount: number } | null };

export type RegisterArtifactSignerMutationVariables = Exact<{
  name: string;
  publicKeyPem: string;
}>;


export type RegisterArtifactSignerMutation = { registerArtifactSigner: { id: string, createdAt: string | null, token: string, name: string, algorithm: SignerAlgorithm, publicKeyPem: string, keyId: string, state: SignerState, revokedAt: string | null, createdBy: string | null, revokedBy: string | null, soleSignedArtifactCount: number } };

export type RevokeArtifactSignerMutationVariables = Exact<{
  token: string;
}>;


export type RevokeArtifactSignerMutation = { revokeArtifactSigner: { id: string, createdAt: string | null, token: string, name: string, algorithm: SignerAlgorithm, publicKeyPem: string, keyId: string, state: SignerState, revokedAt: string | null, createdBy: string | null, revokedBy: string | null, soleSignedArtifactCount: number } };

export type UpdateAssignmentsQueryVariables = Exact<{
  criteria: UpdateAssignmentSearchCriteria;
}>;


export type UpdateAssignmentsQuery = { updateAssignments: { results: Array<{ id: string, createdAt: string | null, token: string, deviceToken: string, component: string, actor: string | null, supersededAt: string | null, current: boolean, artifact: { id: string, createdAt: string | null, updatedAt: string | null, token: string, kind: ArtifactKind, name: string, version: string, component: string, sizeBytes: string, digest: string, mediaType: string, compatibleProfiles: Array<string>, trustStatus: ArtifactTrustStatus, provenance: string | null, state: ArtifactState }, attempt: { token: string, state: UpdateState, targetVersion: string, targetDigest: string, requiresReboot: boolean, lastReportAt: string | null, cancelRequested: boolean, reconciled: boolean, progress: { bytes: string, total: string } | null, failure: { stage: string, code: string, detail: string, platform: boolean } | null }, signedBy: { token: string, name: string, keyId: string, algorithm: SignerAlgorithm, state: SignerState } | null }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type UpdateAssignmentQueryVariables = Exact<{
  token: string;
}>;


export type UpdateAssignmentQuery = { updateAssignment: { id: string, createdAt: string | null, token: string, deviceToken: string, component: string, actor: string | null, supersededAt: string | null, current: boolean, artifact: { id: string, createdAt: string | null, updatedAt: string | null, token: string, kind: ArtifactKind, name: string, version: string, component: string, sizeBytes: string, digest: string, mediaType: string, compatibleProfiles: Array<string>, trustStatus: ArtifactTrustStatus, provenance: string | null, state: ArtifactState }, attempt: { token: string, state: UpdateState, targetVersion: string, targetDigest: string, requiresReboot: boolean, lastReportAt: string | null, cancelRequested: boolean, reconciled: boolean, progress: { bytes: string, total: string } | null, failure: { stage: string, code: string, detail: string, platform: boolean } | null }, signedBy: { token: string, name: string, keyId: string, algorithm: SignerAlgorithm, state: SignerState } | null } | null };

export type AssignArtifactMutationVariables = Exact<{
  deviceToken: string;
  artifactToken: string;
}>;


export type AssignArtifactMutation = { assignArtifact: { id: string, createdAt: string | null, token: string, deviceToken: string, component: string, actor: string | null, supersededAt: string | null, current: boolean, artifact: { id: string, createdAt: string | null, updatedAt: string | null, token: string, kind: ArtifactKind, name: string, version: string, component: string, sizeBytes: string, digest: string, mediaType: string, compatibleProfiles: Array<string>, trustStatus: ArtifactTrustStatus, provenance: string | null, state: ArtifactState }, attempt: { token: string, state: UpdateState, targetVersion: string, targetDigest: string, requiresReboot: boolean, lastReportAt: string | null, cancelRequested: boolean, reconciled: boolean, progress: { bytes: string, total: string } | null, failure: { stage: string, code: string, detail: string, platform: boolean } | null }, signedBy: { token: string, name: string, keyId: string, algorithm: SignerAlgorithm, state: SignerState } | null } };

export type CancelUpdateMutationVariables = Exact<{
  assignmentToken: string;
}>;


export type CancelUpdateMutation = { cancelUpdate: { id: string, createdAt: string | null, token: string, deviceToken: string, component: string, actor: string | null, supersededAt: string | null, current: boolean, artifact: { id: string, createdAt: string | null, updatedAt: string | null, token: string, kind: ArtifactKind, name: string, version: string, component: string, sizeBytes: string, digest: string, mediaType: string, compatibleProfiles: Array<string>, trustStatus: ArtifactTrustStatus, provenance: string | null, state: ArtifactState }, attempt: { token: string, state: UpdateState, targetVersion: string, targetDigest: string, requiresReboot: boolean, lastReportAt: string | null, cancelRequested: boolean, reconciled: boolean, progress: { bytes: string, total: string } | null, failure: { stage: string, code: string, detail: string, platform: boolean } | null }, signedBy: { token: string, name: string, keyId: string, algorithm: SignerAlgorithm, state: SignerState } | null } };

export type AbandonUnknownUpdateMutationVariables = Exact<{
  assignmentToken: string;
  reason: string;
}>;


export type AbandonUnknownUpdateMutation = { abandonUnknownUpdate: { id: string, createdAt: string | null, token: string, deviceToken: string, component: string, actor: string | null, supersededAt: string | null, current: boolean, artifact: { id: string, createdAt: string | null, updatedAt: string | null, token: string, kind: ArtifactKind, name: string, version: string, component: string, sizeBytes: string, digest: string, mediaType: string, compatibleProfiles: Array<string>, trustStatus: ArtifactTrustStatus, provenance: string | null, state: ArtifactState }, attempt: { token: string, state: UpdateState, targetVersion: string, targetDigest: string, requiresReboot: boolean, lastReportAt: string | null, cancelRequested: boolean, reconciled: boolean, progress: { bytes: string, total: string } | null, failure: { stage: string, code: string, detail: string, platform: boolean } | null }, signedBy: { token: string, name: string, keyId: string, algorithm: SignerAlgorithm, state: SignerState } | null } };

export type UpdateRolloutsQueryVariables = Exact<{
  criteria: UpdateRolloutSearchCriteria;
}>;


export type UpdateRolloutsQuery = { updateRollouts: { results: Array<{ id: string, token: string, name: string, groupToken: string, groupVersion: number | null, maxInFlight: number, failureThreshold: number | null, state: UpdateRolloutState, pauseReason: UpdateRolloutPauseReason | null, targetCount: number, createdAt: string | null, createdBy: string | null, startedAt: string | null, finishedAt: string | null, stateChangedBy: string | null, stateChangedAt: string | null, artifact: { token: string, name: string, version: string, component: string, state: ArtifactState, trustStatus: ArtifactTrustStatus }, progress: { total: number, pending: number, inFlight: number, succeeded: number, failed: number, unknown: number, cancelled: number, skipped: number } }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type RolloutFieldsFragment = { id: string, token: string, name: string, groupToken: string, groupVersion: number | null, maxInFlight: number, failureThreshold: number | null, state: UpdateRolloutState, pauseReason: UpdateRolloutPauseReason | null, targetCount: number, createdAt: string | null, createdBy: string | null, startedAt: string | null, finishedAt: string | null, stateChangedBy: string | null, stateChangedAt: string | null, artifact: { token: string, name: string, version: string, component: string, state: ArtifactState, trustStatus: ArtifactTrustStatus }, progress: { total: number, pending: number, inFlight: number, succeeded: number, failed: number, unknown: number, cancelled: number, skipped: number } };

export type UpdateRolloutQueryVariables = Exact<{
  token: string;
}>;


export type UpdateRolloutQuery = { updateRollout: { id: string, token: string, name: string, groupToken: string, groupVersion: number | null, maxInFlight: number, failureThreshold: number | null, state: UpdateRolloutState, pauseReason: UpdateRolloutPauseReason | null, targetCount: number, createdAt: string | null, createdBy: string | null, startedAt: string | null, finishedAt: string | null, stateChangedBy: string | null, stateChangedAt: string | null, artifact: { token: string, name: string, version: string, component: string, state: ArtifactState, trustStatus: ArtifactTrustStatus }, progress: { total: number, pending: number, inFlight: number, succeeded: number, failed: number, unknown: number, cancelled: number, skipped: number } } | null };

export type UpdateRolloutTargetsQueryVariables = Exact<{
  token: string;
  criteria: UpdateRolloutTargetSearchCriteria;
}>;


export type UpdateRolloutTargetsQuery = { updateRolloutTargets: { results: Array<{ deviceToken: string, outcome: UpdateRolloutOutcome, assignmentToken: string | null, attemptState: UpdateState | null, skipCode: string | null, skipDetail: string | null, dispatchedAt: string | null }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } | null };

export type CreateUpdateRolloutMutationVariables = Exact<{
  request: UpdateRolloutCreateRequest;
}>;


export type CreateUpdateRolloutMutation = { createUpdateRollout: { id: string, token: string, name: string, groupToken: string, groupVersion: number | null, maxInFlight: number, failureThreshold: number | null, state: UpdateRolloutState, pauseReason: UpdateRolloutPauseReason | null, targetCount: number, createdAt: string | null, createdBy: string | null, startedAt: string | null, finishedAt: string | null, stateChangedBy: string | null, stateChangedAt: string | null, artifact: { token: string, name: string, version: string, component: string, state: ArtifactState, trustStatus: ArtifactTrustStatus }, progress: { total: number, pending: number, inFlight: number, succeeded: number, failed: number, unknown: number, cancelled: number, skipped: number } } };

export type StartUpdateRolloutMutationVariables = Exact<{
  token: string;
}>;


export type StartUpdateRolloutMutation = { startUpdateRollout: { id: string, token: string, name: string, groupToken: string, groupVersion: number | null, maxInFlight: number, failureThreshold: number | null, state: UpdateRolloutState, pauseReason: UpdateRolloutPauseReason | null, targetCount: number, createdAt: string | null, createdBy: string | null, startedAt: string | null, finishedAt: string | null, stateChangedBy: string | null, stateChangedAt: string | null, artifact: { token: string, name: string, version: string, component: string, state: ArtifactState, trustStatus: ArtifactTrustStatus }, progress: { total: number, pending: number, inFlight: number, succeeded: number, failed: number, unknown: number, cancelled: number, skipped: number } } };

export type PauseUpdateRolloutMutationVariables = Exact<{
  token: string;
}>;


export type PauseUpdateRolloutMutation = { pauseUpdateRollout: { id: string, token: string, name: string, groupToken: string, groupVersion: number | null, maxInFlight: number, failureThreshold: number | null, state: UpdateRolloutState, pauseReason: UpdateRolloutPauseReason | null, targetCount: number, createdAt: string | null, createdBy: string | null, startedAt: string | null, finishedAt: string | null, stateChangedBy: string | null, stateChangedAt: string | null, artifact: { token: string, name: string, version: string, component: string, state: ArtifactState, trustStatus: ArtifactTrustStatus }, progress: { total: number, pending: number, inFlight: number, succeeded: number, failed: number, unknown: number, cancelled: number, skipped: number } } };

export type ResumeUpdateRolloutMutationVariables = Exact<{
  token: string;
  failureThreshold?: number | null | undefined;
}>;


export type ResumeUpdateRolloutMutation = { resumeUpdateRollout: { id: string, token: string, name: string, groupToken: string, groupVersion: number | null, maxInFlight: number, failureThreshold: number | null, state: UpdateRolloutState, pauseReason: UpdateRolloutPauseReason | null, targetCount: number, createdAt: string | null, createdBy: string | null, startedAt: string | null, finishedAt: string | null, stateChangedBy: string | null, stateChangedAt: string | null, artifact: { token: string, name: string, version: string, component: string, state: ArtifactState, trustStatus: ArtifactTrustStatus }, progress: { total: number, pending: number, inFlight: number, succeeded: number, failed: number, unknown: number, cancelled: number, skipped: number } } };

export type AbortUpdateRolloutMutationVariables = Exact<{
  token: string;
}>;


export type AbortUpdateRolloutMutation = { abortUpdateRollout: { id: string, token: string, name: string, groupToken: string, groupVersion: number | null, maxInFlight: number, failureThreshold: number | null, state: UpdateRolloutState, pauseReason: UpdateRolloutPauseReason | null, targetCount: number, createdAt: string | null, createdBy: string | null, startedAt: string | null, finishedAt: string | null, stateChangedBy: string | null, stateChangedAt: string | null, artifact: { token: string, name: string, version: string, component: string, state: ArtifactState, trustStatus: ArtifactTrustStatus }, progress: { total: number, pending: number, inFlight: number, succeeded: number, failed: number, unknown: number, cancelled: number, skipped: number } } };

export class TypedDocumentString<TResult, TVariables>
  extends String
  implements DocumentTypeDecoration<TResult, TVariables>
{
  __apiType?: NonNullable<DocumentTypeDecoration<TResult, TVariables>['__apiType']>;
  private value: string;
  public __meta__?: Record<string, any> | undefined;

  constructor(value: string, __meta__?: Record<string, any> | undefined) {
    super(value);
    this.value = value;
    this.__meta__ = __meta__;
  }

  override toString(): string & DocumentTypeDecoration<TResult, TVariables> {
    return this.value;
  }
}
export const RolloutFieldsFragmentDoc = new TypedDocumentString(`
    fragment RolloutFields on UpdateRollout {
  id
  token
  name
  groupToken
  groupVersion
  maxInFlight
  failureThreshold
  state
  pauseReason
  targetCount
  createdAt
  createdBy
  startedAt
  finishedAt
  stateChangedBy
  stateChangedAt
  artifact {
    token
    name
    version
    component
    state
    trustStatus
  }
  progress {
    total
    pending
    inFlight
    succeeded
    failed
    unknown
    cancelled
    skipped
  }
}
    `, {"fragmentName":"RolloutFields"}) as unknown as TypedDocumentString<RolloutFieldsFragment, unknown>;
export const DeviceUpdateAssignmentsDocument = new TypedDocumentString(`
    query DeviceUpdateAssignments($criteria: UpdateAssignmentSearchCriteria!) {
  updateAssignments(criteria: $criteria) {
    results {
      id
      createdAt
      token
      deviceToken
      component
      artifact {
        id
        token
        kind
        name
        version
        component
        trustStatus
        state
      }
      actor
      supersededAt
      current
      attempt {
        token
        state
        targetVersion
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
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<DeviceUpdateAssignmentsQuery, DeviceUpdateAssignmentsQueryVariables>;
export const DeviceUpdateAssignmentsScopedDocument = new TypedDocumentString(`
    query DeviceUpdateAssignmentsScoped($criteria: UpdateAssignmentSearchCriteria!) {
  updateAssignments(criteria: $criteria) {
    results {
      id
      createdAt
      token
      deviceToken
      component
      artifact {
        id
        token
        kind
        name
        version
        component
        trustStatus
        state
      }
      supersededAt
      current
      attempt {
        token
        state
        targetVersion
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
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<DeviceUpdateAssignmentsScopedQuery, DeviceUpdateAssignmentsScopedQueryVariables>;
export const AssignmentSignerDocument = new TypedDocumentString(`
    query AssignmentSigner($token: String!) {
  updateAssignment(token: $token) {
    token
    signedBy {
      token
      name
      keyId
      algorithm
      state
    }
  }
}
    `) as unknown as TypedDocumentString<AssignmentSignerQuery, AssignmentSignerQueryVariables>;
export const UpdateManagementInfoDocument = new TypedDocumentString(`
    query UpdateManagementInfo {
  updateManagementInfo {
    area
    allowUnverifiedArtifacts
    maxArtifactBytes
    downloadsConfigured
    objectStoreConfigured
  }
}
    `) as unknown as TypedDocumentString<UpdateManagementInfoQuery, UpdateManagementInfoQueryVariables>;
export const ArtifactsDocument = new TypedDocumentString(`
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
    `) as unknown as TypedDocumentString<ArtifactsQuery, ArtifactsQueryVariables>;
export const ArtifactDocument = new TypedDocumentString(`
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
    signatures {
      id
      createdAt
      createdBy
      countsTowardTrust
      signer {
        id
        token
        name
        keyId
        algorithm
        state
      }
    }
  }
}
    `) as unknown as TypedDocumentString<ArtifactQuery, ArtifactQueryVariables>;
export const CreateArtifactDocument = new TypedDocumentString(`
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
    `) as unknown as TypedDocumentString<CreateArtifactMutation, CreateArtifactMutationVariables>;
export const RetireArtifactDocument = new TypedDocumentString(`
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
    `) as unknown as TypedDocumentString<RetireArtifactMutation, RetireArtifactMutationVariables>;
export const AddArtifactSignatureDocument = new TypedDocumentString(`
    mutation AddArtifactSignature($artifactToken: String!, $signerToken: String!, $signature: String!) {
  addArtifactSignature(
    artifactToken: $artifactToken
    signerToken: $signerToken
    signature: $signature
  ) {
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
    `) as unknown as TypedDocumentString<AddArtifactSignatureMutation, AddArtifactSignatureMutationVariables>;
export const ArtifactSignersDocument = new TypedDocumentString(`
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
    `) as unknown as TypedDocumentString<ArtifactSignersQuery, ArtifactSignersQueryVariables>;
export const ArtifactSignerDocument = new TypedDocumentString(`
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
    `) as unknown as TypedDocumentString<ArtifactSignerQuery, ArtifactSignerQueryVariables>;
export const RegisterArtifactSignerDocument = new TypedDocumentString(`
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
    `) as unknown as TypedDocumentString<RegisterArtifactSignerMutation, RegisterArtifactSignerMutationVariables>;
export const RevokeArtifactSignerDocument = new TypedDocumentString(`
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
    `) as unknown as TypedDocumentString<RevokeArtifactSignerMutation, RevokeArtifactSignerMutationVariables>;
export const UpdateAssignmentsDocument = new TypedDocumentString(`
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
    `) as unknown as TypedDocumentString<UpdateAssignmentsQuery, UpdateAssignmentsQueryVariables>;
export const UpdateAssignmentDocument = new TypedDocumentString(`
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
    `) as unknown as TypedDocumentString<UpdateAssignmentQuery, UpdateAssignmentQueryVariables>;
export const AssignArtifactDocument = new TypedDocumentString(`
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
    `) as unknown as TypedDocumentString<AssignArtifactMutation, AssignArtifactMutationVariables>;
export const CancelUpdateDocument = new TypedDocumentString(`
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
    `) as unknown as TypedDocumentString<CancelUpdateMutation, CancelUpdateMutationVariables>;
export const AbandonUnknownUpdateDocument = new TypedDocumentString(`
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
    `) as unknown as TypedDocumentString<AbandonUnknownUpdateMutation, AbandonUnknownUpdateMutationVariables>;
export const UpdateRolloutsDocument = new TypedDocumentString(`
    query UpdateRollouts($criteria: UpdateRolloutSearchCriteria!) {
  updateRollouts(criteria: $criteria) {
    results {
      ...RolloutFields
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    fragment RolloutFields on UpdateRollout {
  id
  token
  name
  groupToken
  groupVersion
  maxInFlight
  failureThreshold
  state
  pauseReason
  targetCount
  createdAt
  createdBy
  startedAt
  finishedAt
  stateChangedBy
  stateChangedAt
  artifact {
    token
    name
    version
    component
    state
    trustStatus
  }
  progress {
    total
    pending
    inFlight
    succeeded
    failed
    unknown
    cancelled
    skipped
  }
}`) as unknown as TypedDocumentString<UpdateRolloutsQuery, UpdateRolloutsQueryVariables>;
export const UpdateRolloutDocument = new TypedDocumentString(`
    query UpdateRollout($token: String!) {
  updateRollout(token: $token) {
    ...RolloutFields
  }
}
    fragment RolloutFields on UpdateRollout {
  id
  token
  name
  groupToken
  groupVersion
  maxInFlight
  failureThreshold
  state
  pauseReason
  targetCount
  createdAt
  createdBy
  startedAt
  finishedAt
  stateChangedBy
  stateChangedAt
  artifact {
    token
    name
    version
    component
    state
    trustStatus
  }
  progress {
    total
    pending
    inFlight
    succeeded
    failed
    unknown
    cancelled
    skipped
  }
}`) as unknown as TypedDocumentString<UpdateRolloutQuery, UpdateRolloutQueryVariables>;
export const UpdateRolloutTargetsDocument = new TypedDocumentString(`
    query UpdateRolloutTargets($token: String!, $criteria: UpdateRolloutTargetSearchCriteria!) {
  updateRolloutTargets(token: $token, criteria: $criteria) {
    results {
      deviceToken
      outcome
      assignmentToken
      attemptState
      skipCode
      skipDetail
      dispatchedAt
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<UpdateRolloutTargetsQuery, UpdateRolloutTargetsQueryVariables>;
export const CreateUpdateRolloutDocument = new TypedDocumentString(`
    mutation CreateUpdateRollout($request: UpdateRolloutCreateRequest!) {
  createUpdateRollout(request: $request) {
    ...RolloutFields
  }
}
    fragment RolloutFields on UpdateRollout {
  id
  token
  name
  groupToken
  groupVersion
  maxInFlight
  failureThreshold
  state
  pauseReason
  targetCount
  createdAt
  createdBy
  startedAt
  finishedAt
  stateChangedBy
  stateChangedAt
  artifact {
    token
    name
    version
    component
    state
    trustStatus
  }
  progress {
    total
    pending
    inFlight
    succeeded
    failed
    unknown
    cancelled
    skipped
  }
}`) as unknown as TypedDocumentString<CreateUpdateRolloutMutation, CreateUpdateRolloutMutationVariables>;
export const StartUpdateRolloutDocument = new TypedDocumentString(`
    mutation StartUpdateRollout($token: String!) {
  startUpdateRollout(token: $token) {
    ...RolloutFields
  }
}
    fragment RolloutFields on UpdateRollout {
  id
  token
  name
  groupToken
  groupVersion
  maxInFlight
  failureThreshold
  state
  pauseReason
  targetCount
  createdAt
  createdBy
  startedAt
  finishedAt
  stateChangedBy
  stateChangedAt
  artifact {
    token
    name
    version
    component
    state
    trustStatus
  }
  progress {
    total
    pending
    inFlight
    succeeded
    failed
    unknown
    cancelled
    skipped
  }
}`) as unknown as TypedDocumentString<StartUpdateRolloutMutation, StartUpdateRolloutMutationVariables>;
export const PauseUpdateRolloutDocument = new TypedDocumentString(`
    mutation PauseUpdateRollout($token: String!) {
  pauseUpdateRollout(token: $token) {
    ...RolloutFields
  }
}
    fragment RolloutFields on UpdateRollout {
  id
  token
  name
  groupToken
  groupVersion
  maxInFlight
  failureThreshold
  state
  pauseReason
  targetCount
  createdAt
  createdBy
  startedAt
  finishedAt
  stateChangedBy
  stateChangedAt
  artifact {
    token
    name
    version
    component
    state
    trustStatus
  }
  progress {
    total
    pending
    inFlight
    succeeded
    failed
    unknown
    cancelled
    skipped
  }
}`) as unknown as TypedDocumentString<PauseUpdateRolloutMutation, PauseUpdateRolloutMutationVariables>;
export const ResumeUpdateRolloutDocument = new TypedDocumentString(`
    mutation ResumeUpdateRollout($token: String!, $failureThreshold: Int) {
  resumeUpdateRollout(token: $token, failureThreshold: $failureThreshold) {
    ...RolloutFields
  }
}
    fragment RolloutFields on UpdateRollout {
  id
  token
  name
  groupToken
  groupVersion
  maxInFlight
  failureThreshold
  state
  pauseReason
  targetCount
  createdAt
  createdBy
  startedAt
  finishedAt
  stateChangedBy
  stateChangedAt
  artifact {
    token
    name
    version
    component
    state
    trustStatus
  }
  progress {
    total
    pending
    inFlight
    succeeded
    failed
    unknown
    cancelled
    skipped
  }
}`) as unknown as TypedDocumentString<ResumeUpdateRolloutMutation, ResumeUpdateRolloutMutationVariables>;
export const AbortUpdateRolloutDocument = new TypedDocumentString(`
    mutation AbortUpdateRollout($token: String!) {
  abortUpdateRollout(token: $token) {
    ...RolloutFields
  }
}
    fragment RolloutFields on UpdateRollout {
  id
  token
  name
  groupToken
  groupVersion
  maxInFlight
  failureThreshold
  state
  pauseReason
  targetCount
  createdAt
  createdBy
  startedAt
  finishedAt
  stateChangedBy
  stateChangedAt
  artifact {
    token
    name
    version
    component
    state
    trustStatus
  }
  progress {
    total
    pending
    inFlight
    succeeded
    failed
    unknown
    cancelled
    skipped
  }
}`) as unknown as TypedDocumentString<AbortUpdateRolloutMutation, AbortUpdateRolloutMutationVariables>;