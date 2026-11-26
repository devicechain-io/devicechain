// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"time"

	"github.com/devicechain-io/dcctl/internal/installflow"
)

// displayGroup is one row of the progress screen. The engine reports its fourteen
// stages; the screen shows the six an operator thinks in, through a static table. The
// overall bar counts these rows, never a percentage of anything.
type displayGroup struct {
	name  string
	steps []installflow.StepID
}

// displayGroups is that table. install.inputs and install.preflight happen before the
// install proper (the preflight screen), and install.summary is the closing report
// ("Finalizing"): none of them is a row, and none counts towards the overall bar.
var displayGroups = []displayGroup{
	{"Resolve target / cluster", []installflow.StepID{installflow.StepInstallCluster}},
	{"Identity, refusals and claim", []installflow.StepID{installflow.StepInstallClaim, installflow.StepInstallInspect}},
	{"Credentials and operator", []installflow.StepID{installflow.StepInstallCredentials,
		installflow.StepInstallPrepare, installflow.StepInstallWriteIntent, installflow.StepInstallOperator}},
	{"Cluster prerequisites", []installflow.StepID{installflow.StepInstallPrerequisites}},
	{"Base identity and record", []installflow.StepID{installflow.StepInstallDatabaseLogin, installflow.StepInstallRecord}},
	{"Relational store ready", []installflow.StepID{installflow.StepInstallRDBReady}},
}

// stageNames is what the "Now" line calls each of the fourteen stages.
var stageNames = map[installflow.StepID]string{
	installflow.StepInstallInputs:        "Resolve the request",
	installflow.StepInstallPreflight:     "Preflight checks",
	installflow.StepInstallCluster:       "Resolve the target cluster",
	installflow.StepInstallClaim:         "Claim the cluster",
	installflow.StepInstallInspect:       "Inspect the cluster",
	installflow.StepInstallCredentials:   "Resolve credentials",
	installflow.StepInstallPrepare:       "Prepare the cluster settings",
	installflow.StepInstallWriteIntent:   "Record the install intent",
	installflow.StepInstallOperator:      "Install the operator",
	installflow.StepInstallPrerequisites: "Apply the cluster prerequisites",
	installflow.StepInstallDatabaseLogin: "Open the database session",
	installflow.StepInstallRecord:        "Record the install",
	installflow.StepInstallRDBReady:      "Wait for the relational store",
	installflow.StepInstallSummary:       "Finalizing",
}

func stageName(id installflow.StepID) string {
	if n, ok := stageNames[id]; ok {
		return n
	}
	return string(id) // a stage this table does not know still shows, by its ID
}

// stepPhase is where one stage's LATEST attempt is.
type stepPhase uint8

const (
	phaseWaiting stepPhase = iota
	phaseRunning
	phaseFinished
	phaseFailed
)

// stepView is the screen's projection of one stage. It holds the latest attempt only:
// a retry starts with no progress at all, so attempt 1's count can never be read as
// attempt 2's.
type stepView struct {
	id       installflow.StepID
	phase    stepPhase
	attempt  int
	started  time.Time
	budget   time.Duration
	outcome  installflow.StepOutcome
	failure  installflow.StepFailed
	metric   *installflow.StepProgress // the latest snapshot of this attempt, if any
	observed time.Time
}

// progressState is everything the progress screen knows, built only from events. It
// decides nothing: a stage is done when the engine closed it, and the run succeeded
// when the summary says so.
type progressState struct {
	runStarted time.Time
	target     string
	dryRun     bool
	manifest   bool
	steps      map[installflow.StepID]*stepView
	order      []installflow.StepID
	active     installflow.StepID // the stage the "Now" block describes
	lastEvent  time.Time
}

func newProgressState() progressState {
	return progressState{steps: map[installflow.StepID]*stepView{}}
}

// apply folds one event in. It mutates the maps it owns; the model copies nothing.
func (s *progressState) apply(e installflow.Envelope) {
	if !e.Time.IsZero() {
		s.lastEvent = e.Time
	}
	switch p := e.Payload.(type) {
	case installflow.RunStarted:
		s.runStarted, s.dryRun, s.manifest = e.Time, p.DryRun, true
		s.target = p.Target.Context.String()
		s.steps, s.order = map[installflow.StepID]*stepView{}, nil
		for _, m := range p.Manifest {
			s.step(m.ID)
		}
	case installflow.StepStarted:
		v := s.step(e.StepID)
		*v = stepView{id: e.StepID, phase: phaseRunning, attempt: e.Attempt, started: e.Time,
			budget: time.Duration(p.BudgetMS) * time.Millisecond}
		s.active = e.StepID
	case installflow.StepProgress:
		v := s.step(e.StepID)
		if v.phase != phaseRunning || v.attempt != e.Attempt {
			return // a late snapshot of an attempt that is already over
		}
		sp := p
		v.metric, v.observed = &sp, e.Time
	case installflow.StepFinished:
		v := s.step(e.StepID)
		if v.attempt == e.Attempt {
			v.phase, v.outcome = phaseFinished, p.Outcome
		}
	case installflow.StepFailed:
		v := s.step(e.StepID)
		if v.attempt == e.Attempt {
			v.phase, v.failure = phaseFailed, p
		}
	}
}

func (s *progressState) step(id installflow.StepID) *stepView {
	v, ok := s.steps[id]
	if !ok {
		v = &stepView{id: id}
		s.steps[id] = v
		s.order = append(s.order, id)
	}
	return v
}

// groupState is a row's word.
type groupState uint8

const (
	groupWaiting groupState = iota
	groupRunning
	groupDone
	groupFailed
	groupStopped
)

// groupView is one row as drawn.
type groupView struct {
	name   string
	state  groupState
	metric *installflow.StepProgress // the running stage's latest snapshot
	// running is set while a stage of the group is active: the bar is live.
	running bool
	outcome installflow.StepOutcome // when done: unchanged/planned if every stage was
}

// groups projects the table onto the manifest. A row none of whose stages are in the
// manifest, or all of whose stages finished not_applicable, is not part of this run:
// it is hidden and not counted.
func (s *progressState) groups() []groupView {
	var out []groupView
	for _, g := range displayGroups {
		var views []*stepView
		applicable := false
		for _, id := range g.steps {
			v, ok := s.steps[id]
			if !ok {
				continue
			}
			views = append(views, v)
			if !(v.phase == phaseFinished && v.outcome == installflow.StepNotApplicable) {
				applicable = true
			}
		}
		if !applicable {
			continue
		}
		gv := groupView{name: g.name}
		done, started := 0, 0
		allUnchanged, allPlanned := true, true
		for _, v := range views {
			switch v.phase {
			case phaseRunning:
				started++
				gv.running = true
				gv.metric = v.metric
			case phaseFinished:
				done++
				started++
				if v.outcome != installflow.StepNotApplicable {
					allUnchanged = allUnchanged && v.outcome == installflow.StepUnchanged
					allPlanned = allPlanned && v.outcome == installflow.StepPlanned
				}
			case phaseFailed:
				started++
				if v.failure.Cancelled {
					gv.state = groupStopped
				} else if gv.state != groupStopped {
					gv.state = groupFailed
				}
				gv.metric = v.metric
			}
		}
		if gv.state == groupWaiting {
			switch {
			case done == len(views):
				gv.state = groupDone
				gv.outcome = installflow.StepSucceeded
				if allPlanned {
					gv.outcome = installflow.StepPlanned
				} else if allUnchanged {
					gv.outcome = installflow.StepUnchanged
				}
			case started > 0:
				gv.state = groupRunning
			}
		}
		out = append(out, gv)
	}
	return out
}

// overall is completed rows out of applicable rows.
func overall(gs []groupView) (done, total int) {
	for _, g := range gs {
		if g.state == groupDone {
			done++
		}
	}
	return done, len(gs)
}

// activeStep is the stage the "Now" block describes, or nil.
func (s *progressState) activeStep() *stepView {
	if s.active == "" {
		return nil
	}
	return s.steps[s.active]
}
