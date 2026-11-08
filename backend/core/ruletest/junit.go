// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ruletest

import (
	"encoding/xml"
	"io"
	"sort"
	"strconv"
	"time"
)

// Case is one reported test case: one expectation, or one file that could not be run.
type Case struct {
	// Suite is the test file the case belongs to.
	Suite string
	Name  string
	// Failure is set when the expectation did not hold.
	Failure string
	// Error is set when the case could not be judged at all (the file did not parse, the
	// evaluation was refused, the server could not be reached).
	Error string
	// Took is how long the case's file took to run; it is attributed to the suite, not the case.
	Took time.Duration
}

// Passed reports whether the case neither failed nor errored.
func (c Case) Passed() bool { return c.Failure == "" && c.Error == "" }

type xmlSuites struct {
	XMLName  xml.Name   `xml:"testsuites"`
	Name     string     `xml:"name,attr"`
	Tests    int        `xml:"tests,attr"`
	Failures int        `xml:"failures,attr"`
	Errors   int        `xml:"errors,attr"`
	Suites   []xmlSuite `xml:"testsuite"`
}

type xmlSuite struct {
	Name     string    `xml:"name,attr"`
	Tests    int       `xml:"tests,attr"`
	Failures int       `xml:"failures,attr"`
	Errors   int       `xml:"errors,attr"`
	Time     string    `xml:"time,attr"`
	Cases    []xmlCase `xml:"testcase"`
}

type xmlCase struct {
	Classname string     `xml:"classname,attr"`
	Name      string     `xml:"name,attr"`
	Time      string     `xml:"time,attr"`
	Failure   *xmlReason `xml:"failure,omitempty"`
	Error     *xmlReason `xml:"error,omitempty"`
}

type xmlReason struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Text    string `xml:",chardata"`
}

// WriteJUnit writes cases as JUnit XML, the format every CI system reads. The report is a pure
// function of the cases: suites and cases are written in the order given, and no timestamp or
// hostname is added, so two runs of the same result produce identical bytes.
func WriteJUnit(w io.Writer, cases []Case) error {
	bySuite := map[string][]Case{}
	var order []string
	for _, c := range cases {
		if _, ok := bySuite[c.Suite]; !ok {
			order = append(order, c.Suite)
		}
		bySuite[c.Suite] = append(bySuite[c.Suite], c)
	}
	sort.Strings(order)
	doc := xmlSuites{Name: "dcctl test"}
	for _, name := range order {
		s := xmlSuite{Name: name}
		var took time.Duration
		for _, c := range bySuite[name] {
			xc := xmlCase{Classname: name, Name: c.Name, Time: "0"}
			if c.Took > took {
				took = c.Took
			}
			switch {
			case c.Error != "":
				xc.Error = &xmlReason{Message: firstLine(c.Error), Type: "error", Text: c.Error}
				s.Errors++
			case c.Failure != "":
				xc.Failure = &xmlReason{Message: firstLine(c.Failure), Type: "expectation", Text: c.Failure}
				s.Failures++
			}
			s.Tests++
			s.Cases = append(s.Cases, xc)
		}
		s.Time = strconv.FormatFloat(took.Seconds(), 'f', 3, 64)
		doc.Tests += s.Tests
		doc.Failures += s.Failures
		doc.Errors += s.Errors
		doc.Suites = append(doc.Suites, s)
	}
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}
