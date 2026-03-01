package workflow

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron field boundaries (standard 5-field cron).
const (
	cronFieldCount = 5
	cronMinuteMax  = 59
	cronHourMax    = 23
	cronDOMMin     = 1
	cronDOMMax     = 31
	cronMonthMin   = 1
	cronMonthMax   = 12
	cronDOWMax     = 7 // 0-7, where 0 and 7 are Sunday
)

// cronFields holds parsed cron sets and flags.
type cronFields struct {
	min, hour, dom, mon, dow map[int]bool
	domAny, dowAny           bool
}

// parseCronExpr parses all 5 cron fields at once.
func parseCronExpr(fields []string) (*cronFields, error) {
	minSet, err := parseCronField(fields[0], 0, cronMinuteMax)
	if err != nil {
		return nil, fmt.Errorf("minute: %w", err)
	}
	hourSet, err := parseCronField(fields[1], 0, cronHourMax)
	if err != nil {
		return nil, fmt.Errorf("hour: %w", err)
	}
	domSet, err := parseCronField(fields[2], cronDOMMin, cronDOMMax)
	if err != nil {
		return nil, fmt.Errorf("day-of-month: %w", err)
	}
	monSet, err := parseCronField(fields[3], cronMonthMin, cronMonthMax)
	if err != nil {
		return nil, fmt.Errorf("month: %w", err)
	}
	dowSet, err := parseCronField(fields[4], 0, cronDOWMax)
	if err != nil {
		return nil, fmt.Errorf("day-of-week: %w", err)
	}

	if dowSet[cronDOWMax] {
		dowSet[0] = true // Sunday as 7
	}

	return &cronFields{
		min: minSet, hour: hourSet, dom: domSet, mon: monSet, dow: dowSet,
		domAny: strings.TrimSpace(fields[2]) == "*",
		dowAny: strings.TrimSpace(fields[4]) == "*",
	}, nil
}

// matchDay checks whether the day-of-month/day-of-week combination matches.
func (cf *cronFields) matchDay(ts time.Time) bool {
	domMatch := cf.dom[ts.Day()]
	dowMatch := cf.dow[int(ts.Weekday())]

	switch {
	case cf.domAny && cf.dowAny:
		return true
	case cf.domAny:
		return dowMatch
	case cf.dowAny:
		return domMatch
	default:
		return domMatch || dowMatch
	}
}

// NextCronRun computes the next run time for a 5-field cron expression.
// Fields: minute hour day-of-month month day-of-week.
// Returns the next run time in milliseconds since epoch.
func NextCronRun(expr, tz string, nowMS int64) (int64, error) {
	fields := strings.Fields(expr)
	if len(fields) != cronFieldCount {
		return 0, errors.New("cron expr must have 5 fields")
	}

	loc := time.Local
	if strings.TrimSpace(tz) != "" {
		l, err := time.LoadLocation(strings.TrimSpace(tz))
		if err != nil {
			return 0, fmt.Errorf("invalid timezone %q", tz)
		}
		loc = l
	}

	cf, err := parseCronExpr(fields)
	if err != nil {
		return 0, err
	}

	start := time.UnixMilli(nowMS).In(loc).Truncate(time.Minute).Add(time.Minute)
	limit := start.Add(366 * 24 * time.Hour)

	for ts := start; !ts.After(limit); ts = ts.Add(time.Minute) {
		if !cf.mon[int(ts.Month())] || !cf.hour[ts.Hour()] || !cf.min[ts.Minute()] {
			continue
		}
		if !cf.matchDay(ts) {
			continue
		}
		return ts.UnixMilli(), nil
	}

	return 0, errors.New("no next run found within 1 year")
}

// addCronRange adds values from start to end (inclusive) with given step.
func addCronRange(out map[int]bool, start, end, step, min, max int) error {
	if step <= 0 {
		return errors.New("step must be > 0")
	}
	if start < min || end > max || start > end {
		return fmt.Errorf("range %d-%d out of bounds [%d-%d]", start, end, min, max)
	}
	for i := start; i <= end; i += step {
		out[i] = true
	}
	return nil
}

// parseCronSegment parses one comma-separated segment (e.g. "1-5/2", "*", "3").
func parseCronSegment(out map[int]bool, p string, min, max int) error {
	base, step := p, 1
	if strings.Contains(p, "/") {
		s := strings.SplitN(p, "/", 2)
		base = strings.TrimSpace(s[0])
		n, err := strconv.Atoi(strings.TrimSpace(s[1]))
		if err != nil {
			return fmt.Errorf("invalid step %q", s[1])
		}
		step = n
	}

	switch {
	case base == "*":
		return addCronRange(out, min, max, step, min, max)
	case strings.Contains(base, "-"):
		b := strings.SplitN(base, "-", 2)
		start, err1 := strconv.Atoi(strings.TrimSpace(b[0]))
		end, err2 := strconv.Atoi(strings.TrimSpace(b[1]))
		if err1 != nil || err2 != nil {
			return fmt.Errorf("invalid range %q", base)
		}
		return addCronRange(out, start, end, step, min, max)
	default:
		n, err := strconv.Atoi(base)
		if err != nil {
			return fmt.Errorf("invalid value %q", base)
		}
		return addCronRange(out, n, n, step, min, max)
	}
}

func parseCronField(expr string, min, max int) (map[int]bool, error) {
	out := make(map[int]bool)
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, errors.New("empty field")
	}

	for _, p := range strings.Split(expr, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			return nil, errors.New("empty segment")
		}
		if err := parseCronSegment(out, p, min, max); err != nil {
			return nil, err
		}
	}
	return out, nil
}
