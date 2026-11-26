// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installflow

// Codes a stopped or failed step attempt carries, and the remedy a stop points
// at. They are identifiers; the words for them are below.
const (
	// ErrorRunCancelled closes the attempt that was active when the run was
	// cancelled and whose work then stopped.
	ErrorRunCancelled ErrorCode = "run.cancelled"
	// ErrorToolAbandoned closes the attempt whose tool — an OpenTofu command —
	// was interrupted and never came back within its budget, so the engine
	// stopped waiting for it. Its effect is never none_observed.
	ErrorToolAbandoned ErrorCode = "run.tool-abandoned"
	// RemedyRerunSameCommand: run the same command again. An install
	// converges, so a re-run finishes what a stopped one left half done.
	RemedyRerunSameCommand MessageCode = "install.rerun-same-command"
	// ErrorPromptUnavailable closes the attempt that needed an answer nobody
	// could give: a confirmation --yes does not settle, or any confirmation
	// without --yes, in a run with no one to ask (the JSON stream never reads
	// stdin; plain output with no terminal answering). The engine asks before
	// the change the question is about, so the attempt changed nothing.
	ErrorPromptUnavailable ErrorCode = "run.prompt-unavailable"
	// RemedyRerunWithYes: run the same command again with --yes, which answers
	// the confirmation that was asked, or choose a target that needs no
	// confirmation (an install into an existing cluster with --kube-context).
	RemedyRerunWithYes MessageCode = "install.rerun-with-yes"
)

// The words a presenter shows while a run stops and after it has, shared so the
// terminal UI, the JSON stream and anything else say the same thing. The plain
// presenter prints none of them: it prints what dcctl always printed, which is
// the install's own error.
const (
	// WordsStopping is the first thing said after an interrupt.
	WordsStopping = "Interrupt received. Waiting for the operation to stop safely."
	// WordsToolStopping is said while the engine waits for an interrupted tool
	// (RunCancelling.WaitingForTool), within RunCancelling.StopBudgetMS.
	WordsToolStopping = "dcctl sent its graceful stop; tofu is writing state."
	// WordsNothingRolledBack is always said of a stop: nothing undoes anything.
	WordsNothingRolledBack = "Nothing is being rolled back."
	// WordsRerunConverges is the recovery RemedyRerunSameCommand names.
	WordsRerunConverges = "Re-running the same command afterwards converges it."
	// WordsSecondInterrupt warns about the force exit a second Ctrl+C is.
	WordsSecondInterrupt = "A second Ctrl+C forces exit and may leave state unrecorded."
	// WordsToolAbandoned is ErrorToolAbandoned. It says what errTofuAbandoned
	// says to the plain output, in the stopping screen's terms: dcctl stopped
	// LOOKING, it did not stop the work.
	WordsToolAbandoned = "dcctl stopped waiting for the interrupted OpenTofu command after its budget. " +
		"It was asked to stop and to write its state, but dcctl did not see it finish, so treat the " +
		"cluster as partly applied, not untouched. Nothing is being rolled back. Re-run the same " +
		"command once dcctl has exited; it converges whatever was left half done."
)

// WordsPromptUnavailable is ErrorPromptUnavailable.
const WordsPromptUnavailable = "The install needed a confirmation and had no one to ask, so it changed nothing. " +
	"Re-run it with --yes to give that confirmation."

// EffectWords says what a step's or a run's effect certainty means.
//
// 🔴 ONLY none_observed MAY BE TOLD AS "NOTHING CHANGED". It is the one value
// the engine reports only when it knows: every other value, and any value this
// function does not know, reads as a possible change.
func EffectWords(e EffectCertainty) string {
	switch e {
	case EffectNoneObserved:
		return "Nothing was changed."
	case EffectApplied:
		return "Changes were applied."
	}
	return "The cluster may be partly changed."
}

// ErrorWords is the sentence for a stop's error code, or "" for a code that is
// the failing stage's own: those are the install's error, which the command
// prints.
func ErrorWords(code ErrorCode) string {
	switch code {
	case ErrorRunCancelled:
		return "Stopped by the interrupt."
	case ErrorToolAbandoned:
		return WordsToolAbandoned
	case ErrorPromptUnavailable:
		return WordsPromptUnavailable
	}
	return ""
}
