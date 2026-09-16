// Package cron parses the five-field cron expressions the manifest accepts
// for backup schedules and computes fire times. It is deliberately the
// classic grammar only: minute, hour, day of month, month, day of week,
// each written as *, a number, a range, a list, or a step. No descriptors
// (@daily), no seconds or years field, no L, W, or # extensions. Fire times
// are computed in UTC; the manifest documents schedules as UTC.
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is one parsed expression. Each field is a bit set of the values
// it matches; domStar and dowStar remember whether the day fields were
// written as * because the classic day rule depends on it.
type Schedule struct {
	expr    string
	minute  uint64
	hour    uint64
	dom     uint64
	month   uint64
	dow     uint64
	domStar bool
	dowStar bool
}

// fieldSpec describes one of the five positions.
type fieldSpec struct {
	name  string
	min   int
	max   int
	names map[string]int
}

var fields = [5]fieldSpec{
	{name: "minute", min: 0, max: 59},
	{name: "hour", min: 0, max: 23},
	{name: "day of month", min: 1, max: 31},
	{name: "month", min: 1, max: 12, names: map[string]int{
		"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
		"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
	}},
	{name: "day of week", min: 0, max: 7, names: map[string]int{
		"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
	}},
}

// searchHorizon bounds Next: an expression that does not fire within this
// span (for example 0 0 31 4 *) never fires.
const searchHorizon = 5 * 366 * 24 * time.Hour

// Parse reads a five-field expression. Errors are plain sentences that
// name the offending field, suitable as manifest diagnostics.
func Parse(expr string) (*Schedule, error) {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return nil, fmt.Errorf("expected 5 fields (minute hour day-of-month month day-of-week), got %d", len(parts))
	}
	s := &Schedule{expr: strings.Join(parts, " ")}
	values := [5]uint64{}
	for i, part := range parts {
		bits, err := parseField(fields[i], part)
		if err != nil {
			return nil, fmt.Errorf("field %d (%s): %w", i+1, fields[i].name, err)
		}
		values[i] = bits
	}
	s.minute, s.hour, s.dom, s.month, s.dow = values[0], values[1], values[2], values[3], values[4]
	// Day of week 7 is Sunday too; fold it onto 0 so matching is uniform.
	if s.dow&(1<<7) != 0 {
		s.dow = (s.dow &^ (1 << 7)) | 1
	}
	s.domStar = parts[2] == "*"
	s.dowStar = parts[4] == "*"
	if s.Next(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)).IsZero() {
		return nil, fmt.Errorf("never fires: no calendar day matches day of month %q with month %q", parts[2], parts[3])
	}
	return s, nil
}

// String returns the normalized expression (single spaces).
func (s *Schedule) String() string { return s.expr }

// Next returns the first fire time strictly after the given instant, in
// UTC, or the zero time when the expression does not fire within five years.
func (s *Schedule) Next(after time.Time) time.Time {
	t := after.UTC().Truncate(time.Minute).Add(time.Minute)
	limit := t.Add(searchHorizon)
	for t.Before(limit) {
		if s.month&(1<<uint(t.Month())) == 0 {
			// Jump to the first minute of the next month.
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
			continue
		}
		if !s.dayMatches(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC)
			continue
		}
		if s.hour&(1<<uint(t.Hour())) == 0 {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, time.UTC)
			continue
		}
		if s.minute&(1<<uint(t.Minute())) == 0 {
			t = t.Add(time.Minute)
			continue
		}
		return t
	}
	return time.Time{}
}

// dayMatches applies the classic rule: when both day fields are
// restricted, a day matches if either does; otherwise the restricted one
// (or both, when both are *) decides.
func (s *Schedule) dayMatches(t time.Time) bool {
	domHit := s.dom&(1<<uint(t.Day())) != 0
	dowHit := s.dow&(1<<uint(t.Weekday())) != 0
	switch {
	case s.domStar && s.dowStar:
		return true
	case s.domStar:
		return dowHit
	case s.dowStar:
		return domHit
	default:
		return domHit || dowHit
	}
}

// parseField turns one comma-separated field into a bit set.
func parseField(spec fieldSpec, field string) (uint64, error) {
	if field == "" {
		return 0, fmt.Errorf("is empty")
	}
	var bits uint64
	for item := range strings.SplitSeq(field, ",") {
		itemBits, err := parseItem(spec, item)
		if err != nil {
			return 0, err
		}
		bits |= itemBits
	}
	return bits, nil
}

// parseItem reads one list element: *, N, a-b, */n, or a-b/n.
func parseItem(spec fieldSpec, item string) (uint64, error) {
	if item == "" {
		return 0, fmt.Errorf("has an empty list element")
	}
	rangePart, stepPart, hasStep := strings.Cut(item, "/")
	step := 1
	if hasStep {
		n, err := strconv.Atoi(stepPart)
		if err != nil || n < 1 {
			return 0, fmt.Errorf("step %q must be a positive number", stepPart)
		}
		step = n
	}

	low, high := spec.min, spec.max
	switch {
	case rangePart == "*":
		// full range
	case strings.Contains(rangePart, "-"):
		lowText, highText, _ := strings.Cut(rangePart, "-")
		var err error
		if low, err = parseValue(spec, lowText); err != nil {
			return 0, err
		}
		if high, err = parseValue(spec, highText); err != nil {
			return 0, err
		}
		if low > high {
			return 0, fmt.Errorf("range %q runs backwards", rangePart)
		}
	default:
		value, err := parseValue(spec, rangePart)
		if err != nil {
			return 0, err
		}
		low = value
		if hasStep {
			// "5/15" means every 15 starting at 5, as most crons read it.
			high = spec.max
		} else {
			high = value
		}
	}

	var bits uint64
	for v := low; v <= high; v += step {
		bits |= 1 << uint(v)
	}
	return bits, nil
}

// parseValue reads a number or a name within the field's range.
func parseValue(spec fieldSpec, text string) (int, error) {
	if spec.names != nil {
		if v, ok := spec.names[strings.ToLower(text)]; ok {
			return v, nil
		}
	}
	v, err := strconv.Atoi(text)
	if err != nil {
		if spec.names != nil {
			return 0, fmt.Errorf("%q is not a number or a name", text)
		}
		return 0, fmt.Errorf("%q is not a number", text)
	}
	if v < spec.min || v > spec.max {
		return 0, fmt.Errorf("%d is out of range %d-%d", v, spec.min, spec.max)
	}
	return v, nil
}
