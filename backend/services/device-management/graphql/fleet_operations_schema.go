// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

// fleetOperationSchema is the fleet-operation fragment of the schema. It is a Go string and not a
// .graphql file on purpose: nothing may publish, mount or document it as served until the engine behind
// it is served, and every tool that walks the schema files (the compatibility check, the published
// reference, the data-access guard) would otherwise treat it as a schema of its own. Descriptions begin
// "Preview." and contain no backtick.
const fleetOperationSchema = `
"""Preview. Where an operation is in dispatching its lines. A paused or dispatched operation is a resting state, not a failure."""
enum FleetOperationDispatch {
  """Preview. Reserved for a later release."""
  DRAFT
  """Preview. The lines are being written."""
  PLANNING
  """Preview. Planned and waiting for a person to start it."""
  READY
  """Preview. Lines are being applied."""
  RUNNING
  """Preview. No line is applied until it is resumed."""
  PAUSED
  """Preview. Every line has been applied or refused; evidence may still be outstanding."""
  DISPATCHED
  """Preview. Stopped for good: lines not yet applied were cancelled."""
  ABORTED
}

"""Preview. Whether the devices have yet answered for the lines that were applied."""
enum FleetOperationEvidence {
  """Preview. At least one applied line has no verdict, or is ambiguous."""
  OPEN
  """Preview. Every applied line has a verdict."""
  CLOSED
}

"""Preview. What happened to a line when it was applied."""
enum FleetLinePosting {
  """Preview. Not applied yet."""
  PLANNED
  """Preview. Reserved for a later release."""
  DISPATCHING
  """Preview. Applied; the device's own answer decides the settlement."""
  POSTED
  """Preview. Not applied: the line was refused, with a code."""
  REFUSED
  """Preview. Not applied: the target no longer qualified."""
  SKIPPED
  """Preview. Not applied: the operation was aborted first."""
  CANCELLED
  """Preview. Reserved for a later release."""
  POSTING_UNKNOWN
}

"""Preview. The device's answer for an applied line."""
enum FleetLineSettlement {
  """Preview. The device reported it applied the value."""
  CONFIRMED
  """Preview. The device reported it rejected the value."""
  FAILED
  """Preview. The device was given a newer configuration before it answered for this one."""
  SUPERSEDED
  """Preview. No answer arrived in time; a person decides."""
  AMBIGUOUS
  """Preview. A person resolved an ambiguous line as confirmed."""
  RESOLVED_CONFIRMED
  """Preview. A person resolved an ambiguous line as failed."""
  RESOLVED_FAILED
}

"""Preview. Whether the engine that applies the operation is making progress."""
enum FleetExecutorState {
  IDLE
  WORKING
  HELD
  STALLED
  UNKNOWN
}

"""Preview. What kind of report decided a line's settlement."""
enum FleetEvidenceBasis {
  """Preview. The full log of accepted reports covers the line."""
  REPORT_LOG
  """Preview. Only the device's latest reported state was available."""
  LATEST_STATE
}

"""Preview. The limits an operation was approved to run under, and the values currently in force."""
type FleetOperationSafety {
  """Preview. The most lines applied per second that was approved."""
  maxPostsPerSecond: Int!
  """Preview. The rate currently in force, never above the approved one."""
  effectivePostsPerSecond: Int!
  """Preview. Failed lines that pause the operation. Null for no limit."""
  failureThreshold: Int
  """Preview. The failure limit currently in force."""
  effectiveFailureThreshold: Int
  """Preview. Ambiguous lines that pause the operation. Null for no limit."""
  ambiguityThreshold: Int
  """Preview. The ambiguity limit currently in force."""
  effectiveAmbiguityThreshold: Int
  """Preview. Seconds a device has to answer before its line becomes ambiguous."""
  settleDeadlineSeconds: Int!
}

"""Preview. What the operation can and cannot claim about the devices' answers."""
type FleetEvidenceCapability {
  """Preview. Whether the broker was configured to refuse rather than drop when the operation started."""
  brokerFailClosed: Boolean!
  """Preview. Whether the history of accepted reports is kept."""
  reportLog: Boolean!
  """Preview. In words, what a verdict does and does not prove."""
  note: String!
}

"""Preview. Line counts of one operation, grouped by state, as of this read."""
type FleetOperationProgress {
  """Preview. Lines not applied yet."""
  planned: Int!
  """Preview. Lines applied."""
  posted: Int!
  """Preview. Lines refused."""
  refused: Int!
  """Preview. Lines skipped."""
  skipped: Int!
  """Preview. Lines cancelled by an abort."""
  cancelled: Int!
  """Preview. Applied lines the device confirmed."""
  confirmed: Int!
  """Preview. Applied lines the device rejected."""
  failed: Int!
  """Preview. Applied lines overtaken by a newer configuration."""
  superseded: Int!
  """Preview. Applied lines with no answer in time."""
  ambiguous: Int!
  """Preview. Ambiguous lines a person has resolved."""
  resolved: Int!
  """Preview. Applied lines still waiting for the device's answer."""
  awaitingEvidence: Int!
}

"""Preview. One change applied to many devices, as one reviewed decision."""
type FleetOperation implements Model & TokenReference {
  """Preview. Server-assigned numeric identifier, as a string."""
  id: ID!
  """Preview. When the operation was created."""
  createdAt: String
  """Preview. When the operation was last written."""
  updatedAt: String
  """Preview. Always null: operations are not soft-deleted."""
  deletedAt: String
  """Preview. Identifier chosen by the creator, unique within the tenant. Creating again with the same token and the same request returns the original."""
  token: String!
  """Preview. Name shown to people."""
  name: String!
  """Preview. What the operation does, for example config.set."""
  kind: String!
  """Preview. Where the operation is in dispatching its lines."""
  dispatch: FleetOperationDispatch!
  """Preview. Whether the devices have answered for every applied line."""
  evidence: FleetOperationEvidence!
  """Preview. Why a paused operation is paused. Null when it is not."""
  pauseReason: String
  """Preview. Why a running operation is waiting. Null when it is not."""
  holdCode: String
  """Preview. Since when it has been waiting."""
  holdSince: String
  """Preview. Whether the engine applying the operation is making progress."""
  executor: FleetExecutorState!
  """Preview. When a line was last applied."""
  lastProgressAt: String
  """Preview. The code of the last error the engine met."""
  lastErrorCode: String
  """Preview. Counts every pause, resume and handover; a pause is final for lines applied before it."""
  executionEpoch: Int!
  """Preview. Digest of the request that created the operation."""
  requestDigest: String!
  """Preview. Digest of the plan: the ordered targets and what was admitted or refused."""
  planDigest: String!
  """Preview. Version of the contract in force."""
  contractVersion: Int!
  """Preview. Digest of the contract a person approves by presenting it at start."""
  contractDigest: String!
  """Preview. The contract in force, as canonical JSON."""
  contract: String!
  """Preview. What the operation does, as canonical JSON."""
  intent: String!
  """Preview. How the targets were named, for example DEVICE_LIST."""
  targetKind: String!
  """Preview. Number of lines, refused ones included."""
  targetCount: Int!
  """Preview. The limits it was approved to run under."""
  safety: FleetOperationSafety!
  """Preview. What its verdicts can and cannot prove."""
  evidenceCapability: FleetEvidenceCapability!
  """Preview. Line counts by state, as of this read."""
  progress: FleetOperationProgress!
  """Preview. Sign-in name of the person the operation currently runs on behalf of. Null until it is started."""
  grantee: String
  """Preview. Sign-in name of the person who created the operation."""
  initiator: String!
  """Preview. When the operation was started."""
  startedAt: String
  """Preview. When its last line was applied."""
  dispatchedAt: String
  """Preview. When the last verdict arrived."""
  closedAt: String
  """Preview. When a READY operation that is never started is aborted."""
  expiresAt: String
}

"""Preview. One device and one change within an operation."""
type FleetOperationLine {
  """Preview. Position in the creator's own input, starting at 1."""
  ordinal: Int!
  """Preview. Token of the target device. Null for an input that named no device and once the device has been deleted."""
  targetToken: String
  """Preview. Whether the line's identifiers were erased."""
  redacted: Boolean!
  """Preview. What happened when the line was applied."""
  posting: FleetLinePosting!
  """Preview. The device's answer, once there is one."""
  settlement: FleetLineSettlement
  """Preview. Why the line was refused or skipped."""
  code: String
  """Preview. Detail for the code."""
  detail: String
  """Preview. The value the device held before, as JSON. Null until the line is applied."""
  prior: String
  """Preview. The value that was applied, as JSON."""
  postedValue: String
  """Preview. The configuration revision the application produced."""
  postedRevision: Int
  """Preview. When the line was applied."""
  postedAt: String
  """Preview. When the device must have answered."""
  settleBy: String
  """Preview. When the line was settled."""
  settledAt: String
  """Preview. What decided the settlement."""
  evidenceBasis: FleetEvidenceBasis
  """Preview. The revision of the report that decided it."""
  evidenceRevision: Int
  """Preview. Set when a later report contradicts a person's verdict."""
  evidenceFlag: String
  """Preview. How many times applying the line was tried."""
  attempts: Int!
  """Preview. The code of the last retryable error."""
  lastErrorCode: String
  """Preview. Whether the line was applied over a change made since the original."""
  overrideChanged: Boolean!
  """Preview. Sign-in name of the person who resolved the line."""
  resolvedBy: String
  """Preview. When it was resolved."""
  resolvedAt: String
}

"""Preview. One page of an operation's lines."""
type FleetLineSearchResults {
  """Preview. The lines on this page, in ordinal order."""
  results: [FleetOperationLine!]!
  """Preview. Where this page sits in the full result."""
  pagination: SearchResultsPagination!
}

"""Preview. One page of operations."""
type FleetOperationSearchResults {
  """Preview. The operations on this page, newest first."""
  results: [FleetOperation!]!
  """Preview. Where this page sits in the full result."""
  pagination: SearchResultsPagination!
}

"""Preview. An input a plan refused."""
type FleetPlanRefusal {
  """Preview. Position in the creator's input."""
  ordinal: Int!
  """Preview. The token as supplied."""
  token: String!
  """Preview. Why: TARGET_NOT_FOUND or DUPLICATE_TARGET."""
  code: String!
}

"""Preview. How many inputs a plan refused for one code."""
type FleetPlanRefusalCount {
  """Preview. The refusal code."""
  code: String!
  """Preview. How many inputs were refused for it, in full."""
  count: Int!
}

"""Preview. What an operation would do, computed without writing anything."""
type FleetOperationPlan {
  """Preview. Lines that would be applied."""
  admittedCount: Int!
  """Preview. Lines that were refused at plan time."""
  refusedCount: Int!
  """Preview. Refusals by code, counted in full."""
  refusalCounts: [FleetPlanRefusalCount!]!
  """Preview. The first 100 refusals of each code, each with the token supplied."""
  refusals: [FleetPlanRefusal!]!
  """Preview. Digest of the request."""
  requestDigest: String!
  """Preview. Digest of the plan."""
  planDigest: String!
  """Preview. Digest to present to start the operation."""
  contractDigest: String!
}

"""Preview. The change a config.set operation makes: one declared SHARED configuration key, set or unset."""
input FleetConfigSetInput {
  """Preview. The configuration key."""
  key: String!
  """Preview. The value, as text. Omit when unsetting."""
  value: String
  """Preview. STRING, LONG, DOUBLE, BOOLEAN or JSON. Omit when unsetting."""
  valueType: String
  """Preview. Remove the key instead of setting it."""
  unset: Boolean
}

"""Preview. The limits an operation asks to run under."""
input FleetOperationCreateSafety {
  """Preview. The most lines applied per second. Default 10; at most 50."""
  maxPostsPerSecond: Int
  """Preview. Failed lines that pause the operation. Omit for no limit."""
  failureThreshold: Int
  """Preview. Ambiguous lines that pause the operation. Omit for no limit."""
  ambiguityThreshold: Int
  """Preview. Seconds a device has to answer before its line becomes ambiguous. Default 86400."""
  settleDeadlineSeconds: Int
}

"""Preview. A request to plan or create a fleet operation."""
input FleetOperationCreateRequest {
  """Preview. Identifier for the operation, unique within the tenant."""
  token: String!
  """Preview. Name shown to people."""
  name: String!
  """Preview. The kind of operation. This release knows config.set."""
  kind: String!
  """Preview. The change, for kind config.set."""
  configSet: FleetConfigSetInput
  """Preview. Tokens of the devices to change, at most 10000. A token that names no device in this tenant becomes a refused line."""
  deviceTokens: [String!]!
  """Preview. The limits to run under."""
  safety: FleetOperationCreateSafety
}

"""Preview. Criteria for listing operations."""
input FleetOperationSearchCriteria {
  """Preview. 1-based page number."""
  pageNumber: Int!
  """Preview. Rows per page."""
  pageSize: Int!
  """Preview. Only operations in this dispatch state."""
  dispatch: String
  """Preview. Only operations in this evidence state."""
  evidence: String
  """Preview. Only operations of this kind."""
  kind: String
}

"""Preview. Criteria for listing one operation's lines."""
input FleetLineSearchCriteria {
  """Preview. 1-based page number."""
  pageNumber: Int!
  """Preview. Rows per page."""
  pageSize: Int!
  """Preview. Only lines in this posting state."""
  posting: String
  """Preview. Only lines with this settlement."""
  settlement: String
  """Preview. Only lines with this code."""
  code: String
}

"""Preview. A change to the limits an operation runs under when it is resumed. Anything looser than the approved contract is refused."""
input FleetOperationSafetyInput {
  """Preview. The most lines applied per second. At most the approved rate."""
  maxPostsPerSecond: Int
  """Preview. Failed lines that pause the operation. At most the approved limit."""
  failureThreshold: Int
  """Preview. Ambiguous lines that pause the operation. At most the approved limit."""
  ambiguityThreshold: Int
  """Preview. Remove the failure limit. Only where the contract has none."""
  clearFailureThreshold: Boolean
  """Preview. Remove the ambiguity limit. Only where the contract has none."""
  clearAmbiguityThreshold: Boolean
}

"""Preview. What resuming an operation with a given change would do, computed without writing anything."""
type FleetResumePreview {
  """Preview. Whether the change stays within what was approved."""
  withinContract: Boolean!
  """Preview. Version of the contract in force."""
  contractVersion: Int!
  """Preview. Digest to present to resume."""
  contractDigest: String!
  """Preview. The contract in force, as canonical JSON."""
  contract: String!
  """Preview. Whether the change needs a fresh second factor. Always false in this release."""
  stepUpRequired: Boolean!
  """Preview. Whether the failure or ambiguity limit is still reached, which refuses a resume."""
  thresholdStillExceeded: Boolean!
}

extend type Query {
  """Preview. One fleet operation by token. Null when the tenant has none. Needs device:read."""
  fleetOperation(token: String!): FleetOperation
  """Preview. Fleet operations, newest first. Needs device:read."""
  fleetOperations(criteria: FleetOperationSearchCriteria!): FleetOperationSearchResults!
  """Preview. The lines of one operation in input order. Needs device:read."""
  fleetOperationLines(token: String!, criteria: FleetLineSearchCriteria!): FleetLineSearchResults!
  """Preview. What an operation would do, without writing anything. Needs device:write and fleet:operate."""
  planFleetOperation(request: FleetOperationCreateRequest!): FleetOperationPlan!
  """Preview. What resuming a paused operation with this change would do, without writing anything. Needs device:write and fleet:operate."""
  previewFleetOperationResume(token: String!, safety: FleetOperationSafetyInput): FleetResumePreview!
}

extend type Mutation {
  """
  Preview. Creates a fleet operation in READY. Creating again with the same token and the same
  request returns the original and writes nothing; a different request under the token is a
  CONFLICT. Needs device:write and fleet:operate.
  """
  createFleetOperation(request: FleetOperationCreateRequest!): FleetOperation!
  """
  Preview. Starts a READY operation. The caller presents the contract digest they reviewed; a different one is
  CONTRACT_DIGEST_MISMATCH. The caller approves the contract and the operation runs on their behalf. Refused with
  FLEET_OPERATIONS_NOT_ACTIVATED when the database does not enforce the device attribute writer protocol. Needs
  device:write and fleet:operate.
  """
  startFleetOperation(token: String!, contractDigest: String!): FleetOperation!
  """
  Preview. Pauses a RUNNING operation. When it returns, no line is applied under the old state. A wait that times out
  is FENCE_PENDING and changes nothing. Needs device:write only, so someone who has lost fleet:operate can still stop it.
  """
  pauseFleetOperation(token: String!): FleetOperation!
  """
  Preview. Resumes a PAUSED operation on the caller's behalf. The digest must be the current contract's. A change to the
  limits is applied only if it stays within the approved contract (CONTRACT_WIDENED otherwise); a limit that is still
  reached is THRESHOLD_STILL_EXCEEDED. Needs device:write and fleet:operate.
  """
  resumeFleetOperation(token: String!, contractDigest: String!, safety: FleetOperationSafetyInput): FleetOperation!
  """
  Preview. Aborts a READY, RUNNING or PAUSED operation: lines not yet applied are cancelled and applied lines keep
  settling. Needs device:write only.
  """
  abortFleetOperation(token: String!): FleetOperation!
}
`
