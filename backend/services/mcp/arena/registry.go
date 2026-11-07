// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package arena

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Bounds on what the in-memory store holds. The arena is a prototype with no database;
// these keep a looping or hostile caller from growing the process without limit.
const (
	maxEpisodes          = 256
	maxScorecardsPerTent = 2000
	episodeTTL           = time.Hour
	maxAgentLabel        = 64
)

// Registry is the arena: the scenario catalog, every live episode, and the scorecards
// taken. One episode is live per caller at a time; resetting replaces it.
type Registry struct {
	cat *Catalog
	now func() time.Time

	mu         sync.Mutex
	episodes   map[string]*Episode // caller -> live episode
	scorecards map[string][]*Scorecard
}

// NewRegistry builds an arena over a catalog.
func NewRegistry(cat *Catalog) *Registry {
	return &Registry{cat: cat, now: time.Now, episodes: map[string]*Episode{}, scorecards: map[string][]*Scorecard{}}
}

// Catalog returns the scenarios the arena serves.
func (r *Registry) Catalog() *Catalog { return r.cat }

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "ep-" + hex.EncodeToString(b[:])
}

// callerKey scopes an episode to the verified caller. The tenant is part of it, so one
// tenant's agent can never address another's episode.
func callerKey(tenant, subject string) string { return tenant + "/" + subject }

// Reset starts an episode for a caller, replacing any live one. An empty scenario starts
// nothing and returns the catalog instead.
func (r *Registry) Reset(tenant, subject, agent, scenario string) (*Observation, []Listing, error) {
	if scenario == "" {
		return nil, r.cat.List(), nil
	}
	sc, spec, err := r.cat.Resolve(scenario)
	if err != nil {
		return nil, nil, err
	}
	agent = strings.TrimSpace(agent)
	if agent == "" {
		agent = "unlabelled"
	}
	if len(agent) > maxAgentLabel {
		agent = agent[:maxAgentLabel]
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evictLocked()
	if _, replacing := r.episodes[callerKey(tenant, subject)]; !replacing && len(r.episodes) >= maxEpisodes {
		return nil, nil, fmt.Errorf("the arena is full (%d live episodes); try again shortly", maxEpisodes)
	}
	ep := newEpisode(newID(), tenant, agent, sc, spec, r.now())
	r.episodes[callerKey(tenant, subject)] = ep
	obs := ep.Observe(nil)
	return &obs, nil, nil
}

func (r *Registry) evictLocked() {
	cut := r.now().Add(-episodeTTL)
	for k, ep := range r.episodes {
		if ep.Started.Before(cut) {
			delete(r.episodes, k)
		}
	}
}

func (r *Registry) live(tenant, subject string) (*Episode, error) {
	ep := r.episodes[callerKey(tenant, subject)]
	if ep == nil {
		return nil, fmt.Errorf("no live episode; call reset with a scenario first")
	}
	return ep, nil
}

// Observe returns the world as of the caller's virtual instant.
func (r *Registry) Observe(tenant, subject string, devices []string) (*Observation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ep, err := r.live(tenant, subject)
	if err != nil {
		return nil, err
	}
	obs := ep.Observe(devices)
	return &obs, nil
}

// Act issues a typed command in the caller's episode.
func (r *Registry) Act(tenant, subject, device, command string, params map[string]any) (ActResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ep, err := r.live(tenant, subject)
	if err != nil {
		return ActResult{}, err
	}
	return ep.Act(device, command, params)
}

// Step advances the caller's clock.
func (r *Registry) Step(tenant, subject string, seconds int) (*Observation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ep, err := r.live(tenant, subject)
	if err != nil {
		return nil, err
	}
	if ep.taken != nil {
		return nil, fmt.Errorf("the scorecard has been taken; reset to play again")
	}
	if ep.Done() {
		return nil, fmt.Errorf("the episode is over; take the scorecard")
	}
	ep.Step(seconds)
	obs := ep.Observe(nil)
	return &obs, nil
}

// Scorecard grades the caller's episode. It is final: the episode takes no more actions
// afterwards. Unless finish is set the episode must have been played to its end, so a
// grade is never taken on a window the agent only half-played by accident.
func (r *Registry) Scorecard(tenant, subject string, finish bool) (*Scorecard, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ep, err := r.live(tenant, subject)
	if err != nil {
		return nil, err
	}
	if ep.taken != nil {
		return ep.taken, nil
	}
	if !ep.Done() {
		if !finish {
			return nil, fmt.Errorf("the episode is at %.0f s of %.0f s; step to the end, or pass finish to let the remaining time run with no further action",
				ep.Now()-ep.spec.T0Sec, ep.spec.DurationSec)
		}
		ep.eng.advanceTo(ep.end)
	}
	card := ep.scorecard()
	ep.taken = card
	list := append(r.scorecards[tenant], card)
	if len(list) > maxScorecardsPerTent {
		list = list[len(list)-maxScorecardsPerTent:]
	}
	r.scorecards[tenant] = list
	return card, nil
}

// Recording writes the caller's finished episode back out as a board recording the
// board-replay player reads, with the agent's actions as chapters.
func (r *Registry) Recording(tenant, subject string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ep, err := r.live(tenant, subject)
	if err != nil {
		return nil, err
	}
	if ep.taken == nil {
		return nil, fmt.Errorf("take the scorecard first; a replay is only written for a finished episode")
	}
	return ep.ExportRecording()
}

// Standing is one agent's result in a suite.
type Standing struct {
	Agent        string  `json:"agent"`
	Suite        string  `json:"suite"`
	Episodes     int     `json:"episodesTaken"`
	Required     int     `json:"episodesRequired"`
	Complete     bool    `json:"complete"`
	Score        float64 `json:"score"`
	WorstEpisode float64 `json:"worstEpisode"`
	Unsafe       int     `json:"unsafeAttempts"`
}

// Leaderboard ranks the agents that have taken scorecards in a tenant, per suite. An
// agent's standing in a suite is built from its BEST scorecard on each episode, so a
// retake can only improve it, and the suite score is the mean over the suite's episodes
// (an episode never taken counts zero, which is why Complete is reported beside it).
func (r *Registry) Leaderboard(tenant, suite string) []Standing {
	r.mu.Lock()
	defer r.mu.Unlock()
	return leaderboard(r.cat, r.scorecards[tenant], suite)
}

func leaderboard(cat *Catalog, cards []*Scorecard, suite string) []Standing {
	required := cat.Suite(suite)
	best := map[string]map[string]*Scorecard{}
	for _, c := range cards {
		if c.Suite != suite {
			continue
		}
		if best[c.Agent] == nil {
			best[c.Agent] = map[string]*Scorecard{}
		}
		if cur := best[c.Agent][c.Scenario]; cur == nil || c.Score > cur.Score {
			best[c.Agent][c.Scenario] = c
		}
	}
	var out []Standing
	for agent, byEp := range best {
		st := Standing{Agent: agent, Suite: suite, Required: len(required), WorstEpisode: 1}
		sum := 0.0
		for _, id := range required {
			c := byEp[id]
			if c == nil {
				st.WorstEpisode = 0
				continue
			}
			st.Episodes++
			sum += c.Score
			st.Unsafe += c.UnsafeAttempts
			if c.Score < st.WorstEpisode {
				st.WorstEpisode = c.Score
			}
		}
		if len(required) > 0 {
			st.Score = round3(sum / float64(len(required)))
		}
		st.Complete = st.Episodes == len(required) && len(required) > 0
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Agent < out[j].Agent
	})
	return out
}

// Agent is the label the caller's live episode was started under.
func (r *Registry) Agent(tenant, subject string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ep := r.episodes[callerKey(tenant, subject)]
	if ep == nil {
		return "", false
	}
	return ep.Agent, true
}

// Autonomy is what a policy says about one command for the agent behind the caller's live
// episode, given everything that agent has been graded on in the tenant so far.
func (r *Registry) Autonomy(tenant, subject string, p Policy, command string) Decision {
	agent, ok := r.Agent(tenant, subject)
	if !ok {
		return Decision{Command: command, Mode: ModeNeedsApproval, Reason: "no live episode"}
	}
	return p.Decide(command, func(suite string) (Standing, bool) { return r.Standing(tenant, suite, agent) })
}

// Commands lists the commands a policy speaks about, sorted, so a scorecard can show
// the decision for each.
func (p Policy) Commands() []string {
	seen := map[string]bool{}
	var out []string
	for _, rule := range p.Rules {
		for _, c := range rule.Commands {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Standing returns one agent's standing in a suite within a tenant.
func (r *Registry) Standing(tenant, suite, agent string) (Standing, bool) {
	for _, s := range r.Leaderboard(tenant, suite) {
		if s.Agent == agent {
			return s, true
		}
	}
	return Standing{}, false
}
