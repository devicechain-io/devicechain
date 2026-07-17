// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"strconv"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
)

// An AI FUNCTION is a job the platform asks a model to do. A tenant assigns one model
// to each function it uses (AIFunctionAssignment), and that assignment is the ANSWER to
// "which model serves this call" — stored, not derived.
//
// WHY THIS EXISTS AT ALL, since the thing it replaced also answered that question. The
// retired mechanism ("the default model") inferred its answer from a property of the
// GRANT SET: the sole model on the menu, the first grant to a tier, whether the tier
// granted anything, whether any mark survived. Every one of those is a set an operator
// can change, so every one of them RE-ANSWERS when they change it — a default that
// moves because a menu grew is not a default, it is a race between an operator's two
// intentions. That defect shipped five separate times, each fix correct about its
// instance and blind to the shape:
//
//   - a sole-model fallback over the UNION: a per-tenant grant vaporised the default
//     the TIER had resolved;
//   - the same fallback scoped to the tier: its twin survived on the TENANT axis;
//   - the same fallback on the tier's own arm: a second unmarked grant turned the door
//     off for every tenant on the tier;
//   - an auto-mark probing the MARK set, which operators can EMPTY: the next grant
//     silently overturned an explicit "no default";
//   - a DORMANT tenant mark that sprang alive the moment its tier was unpackaged.
//
// The fix is not a better derivation. It is to STORE the answer: a row naming a tenant,
// a function, and a provider. Nothing on the read path may consult the size or the
// emptiness of any set — see ResolveModelForFunction, whose entire contract is that
// property.
//
// A function is NOT a caller-supplied parameter. It is decided server-side by which
// resolver is being called (inferRuleCandidate ⇒ FunctionRuleDrafting), because a
// caller that could name its own function would be naming its own entitlement — it
// would shop for whichever function some tenant had assigned the most capable model to.

// Function is one member of the platform's AI-function vocabulary: a job a model can be
// assigned to. Modeled on core/governance's Dimension — declared through register, so
// the declaration site IS the registration site and a restated list cannot drift out of
// step with the declarations.
type Function struct {
	// Token is the stable identifier stored in an assignment row and validated at
	// write. It is platform vocabulary, never operator-authored.
	Token string
	// Name is the human label for a console picker.
	Name string
	// Description says what the function does, for the same picker.
	Description string
}

// allFunctions accumulates every Function declared below, populated by register at
// package init rather than hand-maintained — a list restated beside the declarations is
// a list that silently stops matching them.
var allFunctions []Function

// register records a Function as it is declared and returns it.
func register(f Function) Function {
	allFunctions = append(allFunctions, f)
	return f
}

// FunctionRuleDrafting is the token of the one function the platform declares at GA:
// turning an operator's natural-language description into a candidate DETECT rule
// (ADR-056's NL authoring door). Exported as a constant so the resolver that decides it
// server-side names it in code rather than as a string literal.
const FunctionRuleDrafting = "rule-drafting"

// The AI functions the platform declares.
//
// EXACTLY ONE MEMBER IS DELIBERATE, NOT A PLACEHOLDER. The vocabulary exists because the
// (tenant, function) key is the correct shape for the stored answer, not because a
// second function is planned — and a single-member vocabulary is what makes the shape
// cheap to hold: a second function is a registry entry here plus a settings row per
// tenant that wants it, never a schema change, and never a migration. The alternative
// (storing one model per tenant now, adding the function column later) is the same table
// arrived at through a migration, so declaring the key correctly today costs one struct
// field and saves the migration.
var (
	// RuleDrafting turns natural language into a candidate DETECT rule (ADR-056).
	RuleDrafting = register(Function{
		Token:       FunctionRuleDrafting,
		Name:        "Detection rule drafting",
		Description: "Drafts a candidate DETECT rule from a natural-language description. The draft is a proposal: the CEL compiler and cost gate decide whether it is admissible.",
	})
)

// AllFunctions returns every function the platform declares — the ones registered
// above, not a restatement of them. Returns a COPY: the backing array is package state,
// and handing it out would let one caller's append reach every other caller.
func AllFunctions() []Function {
	out := make([]Function, len(allFunctions))
	copy(out, allFunctions)
	return out
}

// FunctionByToken looks up a declared function by its token.
func FunctionByToken(token string) (Function, bool) {
	for _, f := range allFunctions {
		if f.Token == token {
			return f, true
		}
	}
	return Function{}, false
}

// ValidFunction reports whether token names a declared function. Write paths validate
// against this so an assignment can never name a function nothing will ever ask for —
// a row that looks like a configured capability and is dead.
func ValidFunction(token string) bool {
	_, ok := FunctionByToken(token)
	return ok
}

// AIFunctionAssignment is a tenant's stored answer to "which model serves this
// function": one row per (tenant, function), naming a provider. It is the whole of the
// mechanism that replaced the derived default — see the package note above.
//
// IT IS NOT AN ENTITLEMENT. The assignment says which model the tenant CHOSE; the grant
// tables say which models it MAY use. Keeping them apart is what lets an assignment
// survive a temporary revoke: SetFunctionModel deliberately does not check the menu, and
// ResolveModelForFunction re-checks entitlement on every call. So an operator who
// revokes a model and grants it back does not silently destroy the tenant's choice, and
// a tenant whose entitlement lapses gets NOTHING rather than a substitute.
//
// Tenant-scoped via rdb.TenantScoped, which earns the un-skippable tenant predicate on
// the read path — the same reasoning as AIProviderTenantGrant. The admin plane holds no
// tenant, so operator writes take the sanctioned Api.sys bypass and set TenantId
// explicitly.
//
// NO DeletedAt: rows are hard-deleted. A soft delete would drag in the tombstone-counting
// unique-index bug this repo already carries elsewhere — uix_ai_function_assignment
// would count dead rows and refuse to re-assign a function that was previously cleared.
// The row is a pointer, not a record with a history worth keeping; the audit journal
// records the change independently of the row's survival.
type AIFunctionAssignment struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	rdb.TenantScoped

	// Function is the assigned function's token, validated against the vocabulary at
	// write (ValidFunction).
	Function string `gorm:"not null;size:64"`
	// ProviderID references AIProvider.ID — the immutable id, not the token, so a token
	// rename keeps the assignment bound (the grant tables' reasoning).
	ProviderID uint `gorm:"not null;index"`
}

func (AIFunctionAssignment) TableName() string { return "ai_function_assignments" }

// AuditLabel names the assignment in the ADR-065 decision-7 audit trail as the triple it
// actually is. TenantId is the tenant token; the provider is identified by id because
// the row holds no token.
func (a AIFunctionAssignment) AuditLabel() string {
	return a.TenantId + "/" + a.Function + " → provider#" + strconv.FormatUint(uint64(a.ProviderID), 10)
}
