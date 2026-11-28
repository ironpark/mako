package routines

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule represents a parsed cron or interval schedule.
type Schedule struct {
	raw      string
	interval time.Duration
	minute   [60]bool
	hour     [24]bool
	dom      [32]bool // 1..31
	month    [13]bool // 1..12
	dow      [7]bool  // 0=Sun..6=Sat
	// domStar and dowStar mark a day-of-month or day-of-week field that
	// begins with "*", as Vixie cron does: a day matches both fields when
	// either is so, and either field when neither is.
	domStar bool
	dowStar bool
}

// ParseSchedule parses a standard 5-field cron expression
// ("minute hour day-of-month month day-of-week") or a shorthand descriptor
// (@hourly, @daily, @weekly, @every <duration>). As in Vixie cron, when
// both the day-of-month and the day-of-week fields are restricted (neither
// begins with "*"), a day matches when either field does. An @every
// duration is a whole number of minutes, at least one.
func ParseSchedule(expr string) (Schedule, error) {
	raw := strings.TrimSpace(expr)
	if raw == "" {
		return Schedule{}, errors.New("schedule cannot be empty")
	}
	s := Schedule{raw: raw}
	lower := strings.ToLower(raw)
	switch {
	case lower == "@hourly":
		return ParseSchedule("0 * * * *")
	case lower == "@daily" || lower == "@midnight":
		return ParseSchedule("0 0 * * *")
	case lower == "@weekly":
		return ParseSchedule("0 0 * * 0")
	case strings.HasPrefix(lower, "@every "):
		durStr := strings.TrimSpace(raw[len("@every "):])
		d, err := time.ParseDuration(durStr)
		if err != nil {
			return Schedule{}, fmt.Errorf("invalid @every duration %q: %w", durStr, err)
		}
		if d < time.Minute {
			return Schedule{}, errors.New("@every interval must be at least 1m")
		}
		if d%time.Minute != 0 {
			return Schedule{}, fmt.Errorf("@every interval %s is not a whole number of minutes", d)
		}
		s.interval = d
		return s, nil
	}

	fields := strings.Fields(raw)
	if len(fields) != 5 {
		return Schedule{}, fmt.Errorf("expected 5 cron fields (min hour dom month dow) or @every/@hourly/@daily, got %d", len(fields))
	}
	if err := parseField(fields[0], 0, 59, s.minute[:]); err != nil {
		return Schedule{}, fmt.Errorf("minute field: %w", err)
	}
	if err := parseField(fields[1], 0, 23, s.hour[:]); err != nil {
		return Schedule{}, fmt.Errorf("hour field: %w", err)
	}
	if err := parseField(fields[2], 1, 31, s.dom[:]); err != nil {
		return Schedule{}, fmt.Errorf("day-of-month field: %w", err)
	}
	if err := parseField(fields[3], 1, 12, s.month[:]); err != nil {
		return Schedule{}, fmt.Errorf("month field: %w", err)
	}
	if err := parseDowField(fields[4], s.dow[:]); err != nil {
		return Schedule{}, fmt.Errorf("day-of-week field: %w", err)
	}
	s.domStar = strings.HasPrefix(fields[2], "*")
	s.dowStar = strings.HasPrefix(fields[4], "*")
	return s, nil
}

// String returns the original schedule expression.
func (s Schedule) String() string { return s.raw }

// IsInterval reports whether s is an @every interval schedule.
func (s Schedule) IsInterval() bool { return s.interval > 0 }

// Interval returns the @every duration (zero for calendar cron schedules).
func (s Schedule) Interval() time.Duration { return s.interval }

// Matches reports whether t (truncated to the minute) satisfies a cron
// schedule. For @every schedules, Matches checks alignment to the interval
// since the Unix epoch.
func (s Schedule) Matches(t time.Time) bool {
	if s.interval > 0 {
		return t.Unix()/60%int64(s.interval/time.Minute) == 0
	}
	return s.minute[t.Minute()] &&
		s.hour[t.Hour()] &&
		s.month[int(t.Month())] &&
		s.dayMatches(t)
}

// dayMatches reports whether t's day satisfies the day-of-month and
// day-of-week fields: both when either begins with "*", else either, as
// Vixie cron has it.
func (s Schedule) dayMatches(t time.Time) bool {
	dom, dow := s.dom[t.Day()], s.dow[int(t.Weekday())]
	if s.domStar || s.dowStar {
		return dom && dow
	}
	return dom || dow
}

// Next returns the earliest time strictly after `after` (rounded up to the
// next minute) that matches s, or the zero time if none exists within 4 years.
func (s Schedule) Next(after time.Time) time.Time {
	t := after.Truncate(time.Minute).Add(time.Minute)
	if s.interval > 0 {
		step := s.interval
		rem := time.Duration(t.Unix()%int64(step/time.Second)) * time.Second
		if rem == 0 {
			return t
		}
		return t.Add(step - rem)
	}
	limit := t.Add(4 * 366 * 24 * time.Hour)
	for t.Before(limit) {
		if !s.month[int(t.Month())] {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !s.dayMatches(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !s.hour[t.Hour()] {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, t.Location())
			continue
		}
		if !s.minute[t.Minute()] {
			t = t.Add(time.Minute)
			continue
		}
		return t
	}
	return time.Time{}
}

func parseField(expr string, minVal, maxVal int, dst []bool) error {
	for part := range strings.SplitSeq(expr, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return errors.New("empty list item")
		}
		step := 1
		rangePart := part
		if base, stepStr, hasStep := strings.Cut(part, "/"); hasStep {
			n, err := strconv.Atoi(stepStr)
			if err != nil || n <= 0 {
				return fmt.Errorf("invalid step %q", stepStr)
			}
			step = n
			rangePart = base
		}
		lo, hi := minVal, maxVal
		switch {
		case rangePart == "*":
			// full range
		case strings.Contains(rangePart, "-"):
			a, b, _ := strings.Cut(rangePart, "-")
			var err error
			lo, err = strconv.Atoi(a)
			if err != nil {
				return fmt.Errorf("invalid start %q", a)
			}
			hi, err = strconv.Atoi(b)
			if err != nil {
				return fmt.Errorf("invalid end %q", b)
			}
			if lo < minVal || hi > maxVal || lo > hi {
				return fmt.Errorf("range %d-%d out of bounds [%d,%d]", lo, hi, minVal, maxVal)
			}
		default:
			v, err := strconv.Atoi(rangePart)
			if err != nil {
				return fmt.Errorf("invalid value %q", rangePart)
			}
			if v < minVal || v > maxVal {
				return fmt.Errorf("value %d out of bounds [%d,%d]", v, minVal, maxVal)
			}
			lo = v
			if !strings.Contains(part, "/") {
				hi = v
			}
		}
		for v := lo; v <= hi; v += step {
			dst[v] = true
		}
	}
	return nil
}

func parseDowField(expr string, dst []bool) error {
	var tmp [8]bool
	if err := parseField(expr, 0, 7, tmp[:]); err != nil {
		return err
	}
	copy(dst, tmp[:7])
	if tmp[7] {
		dst[0] = true
	}
	return nil
}
