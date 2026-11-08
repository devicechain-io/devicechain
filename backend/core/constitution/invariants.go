// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package constitution is the machine-readable index of the Substrate Constitution
// (CON-n) and the Citizen Rights red lines (CR-n): for every numbered invariant, the
// test, rig or guard that proves it, and whether that proof exists today or is only
// scheduled.
//
// STATUS: both documents are PROPOSED, not Accepted. They live in the private strategy
// repo (substrate-constitution-adr-draft-2026-11.md, citizen-rights-adr-draft-2026-11.md)
// and take ADR numbers when the maintainer ratifies them. Until then nothing may cite a
// CON-n or CR-n as binding; this table records what WOULD prove each one.
//
// The index exists so the documents cannot drift from the tree in the two directions that
// are silent:
//
//   - an invariant marked Exists whose proof was renamed or deleted keeps reading as
//     enforced while nothing enforces it (TestEveryExistingProofIsLive);
//   - an invariant marked Scheduled whose target release has shipped keeps reading as
//     "coming" forever (TestNoScheduledProofIsPastItsRelease).
//
// A Scheduled proof is NOT enforcement. An invariant whose only proofs are Scheduled does
// not hold today, and no public page may claim it (CON-12).
package constitution

// The number of numbered invariants each document declares. The index must list exactly
// CON-1..CON-ConstitutionArticles and CR-1..CR-CitizenRightsRedLines; change a count only
// with the document.
const (
	ConstitutionArticles  = 20
	CitizenRightsRedLines = 13
)

// Status says whether a proof is on main today.
type Status int

const (
	// Scheduled: the proof's name is reserved and an owning lane is named, but it does not
	// exist yet. The invariant it would prove is not enforced.
	Scheduled Status = iota + 1
	// Exists: the proof is in the tree today, and the index tests check that it still is.
	Exists
)

func (s Status) String() string {
	switch s {
	case Scheduled:
		return "SCHEDULED"
	case Exists:
		return "EXISTS"
	}
	return "UNKNOWN"
}

// Kind says what a proof is and therefore how its existence is checked.
type Kind int

const (
	// GoTest is a top-level `func Name(t *testing.T)` in a _test.go file directly inside
	// Path (a directory relative to the repository root).
	GoTest Kind = iota + 1
	// File is a script or rig at Path (a file relative to the repository root); Name is
	// what it is called in prose.
	File
)

// Proof is one test, rig or guard that proves (part of) an invariant.
type Proof struct {
	Kind Kind
	// Path is the test's directory (GoTest) or the file (File), relative to the repository
	// root, slash-separated. Required when Status is Exists; may be empty while Scheduled.
	Path string
	// Name is the test function name (GoTest) or a human name for the file (File).
	Name string
	// Tags are the build tags the gate that runs a GoTest sets (for example "integration"
	// for a test only hack/migration-diff.sh runs). A test in a file those tags do not
	// select does not count as live.
	Tags   []string
	Status Status
	// Owner is the lane or backlog item that builds a Scheduled proof. Required when
	// Scheduled.
	Owner string
	// Target is the MAJOR.MINOR release a Scheduled proof is due in. Once a release at or
	// past it has shipped the proof must exist or be re-targeted, explicitly, here.
	Target string
}

// Invariant is one numbered article of the Constitution or red line of Citizen Rights.
type Invariant struct {
	ID     string // CON-n or CR-n
	Title  string
	Proofs []Proof
}

func exists(path, name string) Proof {
	return Proof{Kind: GoTest, Path: path, Name: name, Status: Exists}
}

func existsTagged(path, name string, tags ...string) Proof {
	p := exists(path, name)
	p.Tags = tags
	return p
}

func existsFile(path, name string) Proof {
	return Proof{Kind: File, Path: path, Name: name, Status: Exists}
}

func scheduled(name, owner, target string) Proof {
	return Proof{Kind: GoTest, Name: name, Status: Scheduled, Owner: owner, Target: target}
}

func scheduledRig(name, owner, target string) Proof {
	return Proof{Kind: File, Name: name, Status: Scheduled, Owner: owner, Target: target}
}

// Release targets. Items the strategy places past v2.0 (2029 and later) are held at 2.0 on
// purpose: the v2.0 cut is where they must be re-planned, by editing this table, rather
// than sit unexamined for years.
const (
	v13 = "1.3"
	v14 = "1.4"
	v15 = "1.5"
	v16 = "1.6"
	v20 = "2.0"
)

const (
	pkgOAuth      = "backend/core/auth"
	pkgMCP        = "backend/services/mcp/server"
	pkgDMModel    = "backend/services/device-management/model"
	pkgDelivery   = "backend/services/command-delivery/processor"
	pkgDetectCore = "backend/services/event-processing/internal/detect/core"
	pkgEP         = "backend/services/event-processing"
	pkgSelf       = "backend/core/constitution"
	pkgPurgeDrill = "backend/tools/migrationdiff"
)

// Invariants returns the index. A fresh slice each call, so a caller cannot edit the
// table another caller reads.
func Invariants() []Invariant {
	return []Invariant{
		// Article I. The control-tier contract.
		{ID: "CON-1", Title: "Four control tiers; DeviceChain is never tier 0 (no tier-0 member exists to declare)",
			Proofs: []Proof{scheduled("TestNoCommandDefinitionCanDeclareTierZero", "#38 action-class taxonomy on command definitions", v20)}},
		{ID: "CON-2", Title: "Takeover is supervisory only; an adapter writes only to its declared allowlist",
			Proofs: []Proof{scheduled("TestAnAdapterWriteToAnUndeclaredPointIsRefusedAndCounted", "#42 shadow and parity", v20)}},

		// Article II. Action classes.
		{ID: "CON-3", Title: "Every actuation has one action class; never-agent classes cannot be proposed by an agent",
			Proofs: []Proof{
				// Today's floor: no OAuth token carries a write, and a raw DESTRUCTIVE execute is refused.
				exists(pkgOAuth, "TestScopeAllowancesAreReadOnly"),
				exists(pkgDMModel, "TestADestructiveCommandIsHeldToo"),
				exists(pkgDMModel, "TestRawFactoryResetExecuteIsRefused"),
				scheduled("TestEveryCommandDefinitionPathResolvesAnActionClass", "BC-04 ActionRequest", v14),
				scheduled("TestAnAgentOriginTokenCannotProposeANeverAgentClass", "BC-04 ActionRequest", v14),
			}},

		// Article III. Envelopes and permissives.
		{ID: "CON-4", Title: "Every supervisory write passes an operational envelope, enforced at the edge",
			Proofs: []Proof{scheduledRig("TestAnOutOfEnvelopeSetpointIsRefusedAtTheEdgeWhileTheCloudIsDisconnected", "#38 envelope v1", v20)}},
		{ID: "CON-5", Title: "Commands fail into the declared safe state, never into the last command",
			Proofs: []Proof{
				scheduled("TestALapsedPermissiveFallsBackToTheDeclaredSafeValue", "#38 envelope v1", v20),
				scheduled("TestExpiresAtIsNeverPresentedAsAPermissive", "#38 envelope v1", v20),
			}},

		// Article IV. Cells, degradation and drills.
		{ID: "CON-6", Title: "One home cell per actuator; degradation modes declared in advance",
			Proofs: []Proof{
				// Single-instance form: one owner of a device's dispatch at a time.
				exists(pkgDelivery, "TestConcurrentSweepsPublishOnce"),
				exists(pkgDelivery, "TestSweepPublishesNothingWithoutTheLock"),
				scheduledRig("TestNoTwoCellsCommandOneActuator", "#50 cell v1", v20),
			}},
		{ID: "CON-7", Title: "AI-off and network-off are drilled invariants",
			Proofs: []Proof{
				exists(pkgEP, "TestTheDecisionPathImportsNoInferenceClient"),
				scheduledRig("AI-off drill rig (every AI component disabled; tier-1/2 functions still run)", "Constitution follow-up, lane named at ratification", v15),
				scheduledRig("netem network-off rig (24 h cut, zero loss on drain)", "BC-03", v14),
			}},

		// Article V. Determinism, simulation and agents.
		{ID: "CON-8", Title: "Simulation may only veto, never admit",
			Proofs: []Proof{scheduled("TestASimulationResultIsNeverAnAdmittingReason", "#64 simulator gate", v20)}},
		{ID: "CON-9", Title: "No agent actuates except through a gated, recorded ActionRequest",
			Proofs: []Proof{
				// Today's control: there is no agent write path at all.
				exists(pkgOAuth, "TestScopeAllowancesAreReadOnly"),
				exists(pkgMCP, "TestEveryRegisteredToolDeclaresItsRisk"),
				scheduledRig("BC-04 rig: unapproved, replayed and mutated writes refused", "BC-04 ActionRequest", v14),
			}},
		{ID: "CON-10", Title: "Every admit or refuse is a deterministic function of what was recorded",
			Proofs: []Proof{
				exists(pkgDetectCore, "TestReplayCorrectness"),
				scheduled("TestADecisionRecordReplaysToTheSameVerdict", "BC-04 decision record v0", v14),
			}},
		{ID: "CON-11", Title: "Fail loudly on what is not built: an unbuilt guard refuses, it never passes",
			Proofs: []Proof{exists(pkgSelf, "TestNoScheduledProofIsPastItsRelease")}},

		// Article VI. What we say in public.
		{ID: "CON-12", Title: "The narrowed public claims: forbidden phrases never appear on a published surface",
			Proofs: []Proof{existsFile("hack/check-docs-claims.sh", "published-claims guard")}},

		// Article VII. The agent-builder red lines (AX-RL1..RL8).
		{ID: "CON-13", Title: "Agents may draft governance objects but never authorise them",
			Proofs: []Proof{scheduled("TestGovernanceKindCannotBeAgentWritable", "AX-00 change-kind registry (#2307)", v13)}},
		{ID: "CON-14", Title: "An irreversible effect is never auto-approved; every kind declares an effect class",
			Proofs: []Proof{
				scheduled("TestRegisterWithoutEffectClassPanics", "AX-00 change-kind registry (#2307)", v13),
				scheduled("TestAutoEligible", "AX-00 change-kind registry (#2307)", v13),
			}},
		{ID: "CON-15", Title: "A track record only tightens review, never below the code floor; a model change resets it",
			Proofs: []Proof{scheduled("TestAModelEpochBumpRoutesAtTheFloor", "AX-05 oversight at volume", v20)}},
		{ID: "CON-16", Title: "Agent-visible text is platform-authored or tainted",
			Proofs: []Proof{scheduledRig("hack/check-redlines.sh remediation guard", "AX-00 (#2307)", v13)}},
		{ID: "CON-17", Title: "Change-rate ceilings, incident freeze and a correlated-failure breaker",
			Proofs: []Proof{scheduled("TestAChangeOverTheCeilingIsRefusedAndCounted", "AX-05 oversight at volume", v20)}},
		{ID: "CON-18", Title: "Mandates expire in at most 30 days, are typed, and never cover never-agent classes",
			Proofs: []Proof{scheduled("TestAThirtyOneDayMandateIsRefusedAtSigning", "AX-04 agent identity and mandates", v20)}},
		{ID: "CON-19", Title: "Declarative-only: no tenant code runtime",
			Proofs: []Proof{scheduledRig("hack/check-redlines.sh dependency guard", "AX-00 (#2307)", v13)}},
		{ID: "CON-20", Title: "No auto path without holdouts and invariants for the kinds it covers",
			Proofs: []Proof{scheduled("TestTheRouterNeverAutoApprovesAKindWithoutHoldoutsAndInvariants", "AX-05 oversight at volume", v20)}},

		// Citizen Rights, Part A. Red lines.
		{ID: "CR-1", Title: "No silent read: every served read is counted in a tenant-visible ledger; blind spots published",
			Proofs: []Proof{
				scheduled("TestEveryServedReadPathIsEnumeratedInTheReadLedger", "BC-09 civic data spine", v15),
				scheduled("TestTheRegisterNamesEveryPathTheLedgerCannotSee", "BC-09 civic data spine", v15),
			}},
		{ID: "CR-2", Title: "No covert operator entry: a tenant-visible, hash-chained break-glass row",
			Proofs: []Proof{
				scheduled("TestASuperuserEntryIntoANonMemberTenantWritesATenantVisibleRow", "#26 break-glass log", v15),
				scheduled("TestABreakGlassRowEditFailsVerify", "#26 break-glass log", v15),
			}},
		{ID: "CR-3", Title: "Personal data is classed, the class travels, and unclassified is personal",
			Proofs: []Proof{
				scheduled("TestEveryMetricAndCommandResolvesAPersonalDataClass", "BC-09 civic data spine", v15),
				scheduled("TestAnUnclassifiedMetricIsPersonalOnEverySharingPath", "BC-09 civic data spine", v15),
			}},
		{ID: "CR-4", Title: "Position stays its own authority",
			Proofs: []Proof{
				exists(pkgMCP, "TestAPositionToolIsNotReachableOnTheGeneralReadScope"),
				exists(pkgOAuth, "TestScopeAllowancesAreExactlyThese"),
				scheduled("TestAnOfferNamingPositionWithoutLocationAuthorityIsRefused", "#34 sharing agreements", v20),
			}},
		{ID: "CR-5", Title: "Public outputs never carry personal data or raw position",
			Proofs: []Proof{
				scheduled("TestAPublicOfferCarryingPersonalOrPositionDataIsRefusedAtAuthoring", "#21 public dashboards", v15),
				scheduled("TestAPublicCountBelowTheKFloorIsSuppressed", "#21 public dashboards", v15),
			}},
		{ID: "CR-6", Title: "A sharing token can never write or actuate",
			Proofs: []Proof{scheduled("TestAnAgreementTokenIsRefusedByEveryWriteResolver", "#34 sharing agreements", v20)}},
		{ID: "CR-7", Title: "The project ships nothing whose purpose is identifying people",
			Proofs: []Proof{scheduled("TestNoShippedPackDeclaresAnIdentificationCapability", "BC-07 vocabulary packs", v15)}},
		{ID: "CR-8", Title: "Automated public decisions are explainable (deterministically) and contestable",
			Proofs: []Proof{
				scheduled("TestAnExplanationIsGeneratedFromTheCompiledRuleAlone", "#27 recorded inference and explain", v15),
				scheduled("TestATranslationThatChangesAThresholdFailsTheBackCheck", "#58 contest workflow", v20),
			}},

		// Citizen Rights, Part B. Erasure versus replay.
		{ID: "CR-9", Title: "Erasure wins over replay; a sealed record verifies but never replays",
			Proofs: []Proof{
				// Today's erasure: whole-tenant purge only.
				// Runs in CI under hack/migration-diff.sh, which sets -tags integration.
				existsTagged(pkgPurgeDrill, "TestTheSweepRetainsAndRedactsTheJournalThroughTheStore", "integration"),
				scheduled("TestASealedRecordVerifiesButDoesNotReplay", "BC-04 decision record v0 / #56", v20),
				scheduled("TestReplayOfASealedRecordReturnsRecordSealed", "BC-04 decision record v0 / #56", v20),
			}},

		// Citizen Rights, Part C. The custodian seam.
		{ID: "CR-10", Title: "Personal-class keys can sit outside the operator; an unreachable custodian fails closed",
			Proofs: []Proof{
				scheduled("TestAPersonalClassReadWithoutACustodianReleaseFailsClosed", "#56 custodian KEK", v20),
				scheduled("TestTheCustodianProviderNeverFallsBackToTheInstanceKey", "#56 custodian KEK", v20),
			}},

		// Citizen Rights, Part D. Compelled access and disclosure tiers.
		{ID: "CR-11", Title: "A compelled disclosure is logged, counted and disclosed later; never silent",
			Proofs: []Proof{scheduled("TestACompelledExportWritesASealedRecordWithAReleaseDate", "#58 transparency", v20)}},
		{ID: "CR-12", Title: "Disclosure tiers: the public tier carries commitments, never an attack map",
			Proofs: []Proof{scheduled("TestThePublicTierCarriesRootsAndCountsNeverContents", "#58 transparency", v20)}},

		// Citizen Rights, Part E. Tamper evidence.
		{ID: "CR-13", Title: "Logs that carry rights are hash-chained and anchored outside the operator",
			Proofs: []Proof{scheduled("TestARewrittenJournalRowBreaksThePublishedRoot", "BC-04 decision record v0", v14)}},
	}
}
