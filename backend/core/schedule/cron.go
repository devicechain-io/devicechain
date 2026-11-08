// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schedule

import (
	"fmt"
	"strconv"
	"strings"
)

// cronExpr is a parsed five-field cron expression: minute hour day-of-month month
// day-of-week. Each field is a bitset of the values it allows.
type cronExpr struct {
	minute uint64 // 0-59
	hour   uint64 // 0-23
	dom    uint64 // 1-31
	month  uint64 // 1-12
	dow    uint64 // 0-6, Sunday = 0
	// domStar / dowStar record whether the field was a bare "*". Standard cron semantics:
	// when BOTH day fields are restricted a day matches if EITHER does; when one is "*" only
	// the other counts.
	domStar, dowStar bool
}

type fieldSpec struct {
	name     string
	min, max int
}

var cronFields = [5]fieldSpec{
	{"minute", 0, 59}, {"hour", 0, 23}, {"day-of-month", 1, 31}, {"month", 1, 12}, {"day-of-week", 0, 7},
}

// parseCron reads a five-field expression. It accepts "*", "a", "a-b", "*/n", "a-b/n" and
// comma lists of those, and nothing else: no names, no "?", no "L", no "@daily". Anything
// outside that is refused rather than guessed at, because a schedule that fires at an
// interpretation of a typo is a light that goes out at the wrong hour.
func parseCron(expr string) (*cronExpr, error) {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return nil, invalidf("cron %q must have exactly 5 fields (minute hour day-of-month month day-of-week)", expr)
	}
	var sets [5]uint64
	for i, p := range parts {
		s, err := parseField(p, cronFields[i])
		if err != nil {
			return nil, invalidf("cron %q: %v", expr, err)
		}
		sets[i] = s
	}
	// Day-of-week 7 is Sunday, an alias of 0.
	if sets[4]&(1<<7) != 0 {
		sets[4] = sets[4]&^(1<<7) | 1
	}
	return &cronExpr{
		minute: sets[0], hour: sets[1], dom: sets[2], month: sets[3], dow: sets[4],
		domStar: parts[2] == "*", dowStar: parts[4] == "*",
	}, nil
}

func parseField(field string, spec fieldSpec) (uint64, error) {
	var set uint64
	for _, item := range strings.Split(field, ",") {
		if item == "" {
			return 0, fmt.Errorf("%s field has an empty list entry", spec.name)
		}
		rangePart, step := item, 1
		if i := strings.IndexByte(item, '/'); i >= 0 {
			rangePart = item[:i]
			n, err := strconv.Atoi(item[i+1:])
			if err != nil || n < 1 {
				return 0, fmt.Errorf("%s field step %q is not a positive integer", spec.name, item[i+1:])
			}
			step = n
		}
		lo, hi := spec.min, spec.max
		switch {
		case rangePart == "*":
		case strings.Contains(rangePart, "-"):
			a, b, _ := strings.Cut(rangePart, "-")
			var err1, err2 error
			lo, err1 = strconv.Atoi(a)
			hi, err2 = strconv.Atoi(b)
			if err1 != nil || err2 != nil {
				return 0, fmt.Errorf("%s field range %q is not two integers", spec.name, rangePart)
			}
		default:
			v, err := strconv.Atoi(rangePart)
			if err != nil {
				return 0, fmt.Errorf("%s field value %q is not an integer", spec.name, rangePart)
			}
			lo, hi = v, v
			if strings.Contains(item, "/") {
				hi = spec.max // "5/15" means from 5 to the end, stepping 15
			}
		}
		if lo < spec.min || hi > spec.max || lo > hi {
			return 0, fmt.Errorf("%s field %q is outside %d..%d", spec.name, item, spec.min, spec.max)
		}
		for v := lo; v <= hi; v += step {
			set |= 1 << uint(v)
		}
	}
	return set, nil
}

// dayMatches reports whether a civil date matches the month and the two day fields.
func (c *cronExpr) dayMatches(month, dom, dow int) bool {
	if c.month&(1<<uint(month)) == 0 {
		return false
	}
	domOK := c.dom&(1<<uint(dom)) != 0
	dowOK := c.dow&(1<<uint(dow)) != 0
	switch {
	case c.domStar && c.dowStar:
		return true
	case c.domStar:
		return dowOK
	case c.dowStar:
		return domOK
	default:
		return domOK || dowOK
	}
}
