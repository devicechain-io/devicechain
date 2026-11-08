// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ruletest

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// TestSuffix is what marks a file as a test file.
const TestSuffix = ".test.yaml"

// DefaultScenario is the scenario a test file runs when it names none.
const DefaultScenario = "scenario.yaml"

// maxFileBytes bounds any file read, so a path that points at a huge file fails fast.
const maxFileBytes = 64 << 20

// Loaded is a scenario read from disk with its events generated, ready to be sent for evaluation.
type Loaded struct {
	Path     string
	Scenario *Scenario
	Events   *Generated
	// Draft is the contents of the draft rule file when the scenario evaluates a draft.
	Draft string
	Err   error
}

// Suite is one test file and the scenario it runs.
type Suite struct {
	Path         string
	Name         string
	ScenarioPath string
	Test         *TestFile
	Err          error
}

// Plan is everything a run will do, read from disk. Nothing in it has touched a server.
type Plan struct {
	Suites []*Suite
	// Scenarios is keyed by absolute path; several suites share one scenario, which is evaluated
	// once.
	Scenarios map[string]*Loaded
}

// Load reads root, which is a directory (searched recursively for *.test.yaml) or one test file.
// A file that does not parse does not stop the load: it is recorded on its suite, so the run reports
// it as an error beside the suites that did run. Load fails only when it finds no test file at all,
// because a run that tests nothing must not look like a run that passed.
func Load(root string) (*Plan, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	var files []string
	if info.IsDir() {
		err = filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
			if werr != nil {
				return werr
			}
			if !d.IsDir() && strings.HasSuffix(d.Name(), TestSuffix) {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	} else {
		if !strings.HasSuffix(root, TestSuffix) {
			return nil, fmt.Errorf("%s is not a test file: test files end in %s", root, TestSuffix)
		}
		files = []string{root}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no %s file was found under %s; a run with nothing to check would pass for the wrong reason", TestSuffix, root)
	}
	sort.Strings(files)

	plan := &Plan{Scenarios: map[string]*Loaded{}}
	for _, f := range files {
		plan.Suites = append(plan.Suites, loadSuite(f, plan))
	}
	return plan, nil
}

func readBounded(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxFileBytes {
		return nil, fmt.Errorf("%s is %d bytes, above the limit of %d", path, info.Size(), maxFileBytes)
	}
	return os.ReadFile(path)
}

func loadSuite(path string, plan *Plan) *Suite {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	s := &Suite{Path: path, Name: filepath.ToSlash(path)}
	data, err := readBounded(path)
	if err != nil {
		s.Err = err
		return s
	}
	tf, err := ParseTestFile(data)
	if err != nil {
		s.Err = fmt.Errorf("%s: %w", path, err)
		return s
	}
	s.Test = tf
	if tf.Name != "" {
		s.Name = tf.Name + " (" + filepath.ToSlash(path) + ")"
	}
	rel := tf.Scenario
	if rel == "" {
		rel = DefaultScenario
	}
	scenarioPath, err := SafeJoin(filepath.Dir(abs), rel)
	if err != nil {
		s.Err = fmt.Errorf("%s: scenario: %w", path, err)
		return s
	}
	s.ScenarioPath = scenarioPath
	if _, ok := plan.Scenarios[scenarioPath]; !ok {
		plan.Scenarios[scenarioPath] = loadScenario(scenarioPath)
	}
	if l := plan.Scenarios[scenarioPath]; l.Err != nil {
		s.Err = fmt.Errorf("%s: %w", path, l.Err)
	} else if err := checkDevices(tf, l.Scenario); err != nil {
		s.Err = fmt.Errorf("%s: %w", path, err)
	}
	return s
}

// checkDevices refuses an expectation that names a device its scenario does not define. Such an
// expectation tests a typo, not a rule: a neverRaised on it is vacuously true and a raised on it is
// certainly false, whatever the rule does.
func checkDevices(tf *TestFile, sc *Scenario) error {
	defined := map[string]bool{}
	for _, d := range sc.Devices {
		defined[d.Token] = true
	}
	for i, e := range tf.Expect {
		var device string
		switch {
		case e.Raised != nil:
			device = e.Raised.Device
		case e.Cleared != nil:
			device = e.Cleared.Device
		case e.NeverRaised != nil:
			device = e.NeverRaised.Device
		case e.Intent != nil:
			device = e.Intent.Device
		case e.NeverIntent != nil:
			device = e.NeverIntent.Device
		}
		if device != "" && !defined[device] {
			return fmt.Errorf("expect[%d]: device %q is not in the scenario", i, device)
		}
	}
	return nil
}

func loadScenario(path string) *Loaded {
	l := &Loaded{Path: path}
	data, err := readBounded(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			l.Err = fmt.Errorf("the scenario %s does not exist", path)
		} else {
			l.Err = err
		}
		return l
	}
	sc, err := ParseScenario(data)
	if err != nil {
		l.Err = fmt.Errorf("scenario %s: %w", path, err)
		return l
	}
	l.Scenario = sc
	dir := filepath.Dir(path)
	read := func(rel string) ([]byte, error) {
		p, err := SafeJoin(dir, rel)
		if err != nil {
			return nil, err
		}
		return readBounded(p)
	}
	if sc.Draft != nil {
		name := sc.Draft.RuleDefinition
		if name == "" {
			name = sc.Draft.Graph
		}
		b, err := read(name)
		if err != nil {
			l.Err = fmt.Errorf("scenario %s: draft: %w", path, err)
			return l
		}
		l.Draft = string(b)
	}
	gen, err := sc.Generate(read)
	if err != nil {
		l.Err = fmt.Errorf("scenario %s: %w", path, err)
		return l
	}
	l.Events = gen
	return l
}
