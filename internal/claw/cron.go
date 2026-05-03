package claw

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// nextCronTime computes the next firing time for a schedule spec, strictly
// after `from`. The result is always in UTC (matching how state stores
// timestamps). When the spec involves a wall-clock time (@daily, @at HH:MM,
// @dow, 5-field cron) the schedule is anchored in `loc` so "every day at
// 09:00" means 09:00 in the user's timezone, not 09:00 UTC. Pass nil loc
// for UTC.
//
// Supported syntaxes:
//
//	@every <duration>     fixed interval ("5m", "1h30m", "24h")
//	@minutely             top of every minute
//	@hourly               top of every hour (HH:00)
//	@daily                every day at 00:00 in loc
//	@weekly               every Monday at 00:00 in loc
//	@at HH:MM             every day at HH:MM in loc
//	@dow <DAY> HH:MM      every <DAY> at HH:MM in loc (Mon|Tue|...|Sun)
//	M H DOM MON DOW       5-field cron (numbers, *, lists, ranges, /step)
func nextCronTime(spec string, from time.Time, loc *time.Location) (time.Time, error) {
	if loc == nil {
		loc = time.UTC
	}
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return time.Time{}, fmt.Errorf("schedule is empty")
	}
	from = from.UTC()

	switch {
	case strings.HasPrefix(spec, "@every "):
		raw := strings.TrimSpace(strings.TrimPrefix(spec, "@every "))
		dur, err := time.ParseDuration(raw)
		if err != nil {
			return time.Time{}, fmt.Errorf("@every: %w", err)
		}
		if dur <= 0 {
			return time.Time{}, fmt.Errorf("duration must be positive")
		}
		return from.Add(dur), nil

	case spec == "@minutely":
		return from.Truncate(time.Minute).Add(time.Minute), nil

	case spec == "@hourly":
		return from.Truncate(time.Hour).Add(time.Hour), nil

	case spec == "@daily":
		return nextDailyAt(from, loc, 0, 0), nil

	case spec == "@weekly":
		return nextWeeklyAt(from, loc, time.Monday, 0, 0), nil

	case strings.HasPrefix(spec, "@at "):
		hh, mm, err := parseHHMM(strings.TrimSpace(strings.TrimPrefix(spec, "@at ")))
		if err != nil {
			return time.Time{}, err
		}
		return nextDailyAt(from, loc, hh, mm), nil

	case strings.HasPrefix(spec, "@dow "):
		rest := strings.TrimSpace(strings.TrimPrefix(spec, "@dow "))
		fields := strings.Fields(rest)
		if len(fields) != 2 {
			return time.Time{}, fmt.Errorf("@dow expects '<DAY> HH:MM', got %q", rest)
		}
		dow, err := parseDOW(fields[0])
		if err != nil {
			return time.Time{}, err
		}
		hh, mm, err := parseHHMM(fields[1])
		if err != nil {
			return time.Time{}, err
		}
		return nextWeeklyAt(from, loc, dow, hh, mm), nil
	}

	if fields := strings.Fields(spec); len(fields) == 5 {
		return nextCronExpr(fields, from, loc)
	}
	return time.Time{}, fmt.Errorf("unsupported schedule %q (try @every, @daily, @at HH:MM, @dow MON 09:00, or 'M H DOM MON DOW')", spec)
}

func parseHHMM(s string) (int, int, error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("expected HH:MM, got %q", s)
	}
	hh, err := strconv.Atoi(parts[0])
	if err != nil || hh < 0 || hh > 23 {
		return 0, 0, fmt.Errorf("invalid hour in %q", s)
	}
	mm, err := strconv.Atoi(parts[1])
	if err != nil || mm < 0 || mm > 59 {
		return 0, 0, fmt.Errorf("invalid minute in %q", s)
	}
	return hh, mm, nil
}

func parseDOW(s string) (time.Weekday, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "sun", "sunday", "0", "7":
		return time.Sunday, nil
	case "mon", "monday", "1":
		return time.Monday, nil
	case "tue", "tues", "tuesday", "2":
		return time.Tuesday, nil
	case "wed", "weds", "wednesday", "3":
		return time.Wednesday, nil
	case "thu", "thur", "thurs", "thursday", "4":
		return time.Thursday, nil
	case "fri", "friday", "5":
		return time.Friday, nil
	case "sat", "saturday", "6":
		return time.Saturday, nil
	}
	return time.Sunday, fmt.Errorf("invalid day of week %q", s)
}

func nextDailyAt(from time.Time, loc *time.Location, hh, mm int) time.Time {
	local := from.In(loc)
	candidate := time.Date(local.Year(), local.Month(), local.Day(), hh, mm, 0, 0, loc)
	if !candidate.After(local) {
		candidate = candidate.AddDate(0, 0, 1)
	}
	return candidate.UTC()
}

func nextWeeklyAt(from time.Time, loc *time.Location, dow time.Weekday, hh, mm int) time.Time {
	local := from.In(loc)
	delta := (int(dow) - int(local.Weekday()) + 7) % 7
	candidate := time.Date(local.Year(), local.Month(), local.Day()+delta, hh, mm, 0, 0, loc)
	if !candidate.After(local) {
		candidate = candidate.AddDate(0, 0, 7)
	}
	return candidate.UTC()
}

// nextCronExpr handles a useful subset of 5-field cron syntax. Each field
// supports:
//
//	*          any value
//	N          a single number
//	A,B,C      list
//	A-B        inclusive range
//	*/N        step over the entire allowed range
//	A-B/N      step within a range
//
// Out of scope: month/dow names ("JAN", "MON"), L, W, #, ?. Numbers only.
// Sunday is 0 in DOW (some crontabs accept 7 — we accept that too at parse
// time and fold into 0).
func nextCronExpr(fields []string, from time.Time, loc *time.Location) (time.Time, error) {
	mins, err := parseCronField(fields[0], 0, 59)
	if err != nil {
		return time.Time{}, fmt.Errorf("minute: %w", err)
	}
	hours, err := parseCronField(fields[1], 0, 23)
	if err != nil {
		return time.Time{}, fmt.Errorf("hour: %w", err)
	}
	doms, err := parseCronField(fields[2], 1, 31)
	if err != nil {
		return time.Time{}, fmt.Errorf("dom: %w", err)
	}
	months, err := parseCronField(fields[3], 1, 12)
	if err != nil {
		return time.Time{}, fmt.Errorf("month: %w", err)
	}
	dows, err := parseCronField(fields[4], 0, 6)
	if err != nil {
		return time.Time{}, fmt.Errorf("dow: %w", err)
	}

	t := from.In(loc).Truncate(time.Minute).Add(time.Minute)
	deadline := t.AddDate(5, 0, 0)
	for ; t.Before(deadline); t = t.Add(time.Minute) {
		if !mins[t.Minute()] {
			continue
		}
		if !hours[t.Hour()] {
			continue
		}
		if !months[int(t.Month())] {
			continue
		}
		if !doms[t.Day()] {
			continue
		}
		if !dows[int(t.Weekday())] {
			continue
		}
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("schedule never fires within 5y window")
}

func parseCronField(field string, min, max int) (map[int]bool, error) {
	out := map[int]bool{}
	field = strings.TrimSpace(field)
	if field == "" {
		return nil, fmt.Errorf("empty field")
	}
	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		step := 1
		if idx := strings.Index(part, "/"); idx >= 0 {
			s, err := strconv.Atoi(strings.TrimSpace(part[idx+1:]))
			if err != nil || s <= 0 {
				return nil, fmt.Errorf("invalid step in %q", part)
			}
			step = s
			part = strings.TrimSpace(part[:idx])
		}
		var lo, hi int
		switch {
		case part == "" || part == "*":
			lo, hi = min, max
		case strings.Contains(part, "-"):
			dash := strings.Index(part, "-")
			a, err := strconv.Atoi(strings.TrimSpace(part[:dash]))
			if err != nil {
				return nil, fmt.Errorf("invalid range start %q", part)
			}
			b, err := strconv.Atoi(strings.TrimSpace(part[dash+1:]))
			if err != nil {
				return nil, fmt.Errorf("invalid range end %q", part)
			}
			lo, hi = a, b
		default:
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("invalid number %q", part)
			}
			// Cron quirk: dow=7 is also Sunday.
			if max == 6 && n == 7 {
				n = 0
			}
			lo, hi = n, n
		}
		if max == 6 && lo == 7 {
			lo = 0
		}
		if max == 6 && hi == 7 {
			hi = 0
		}
		if lo < min || hi > max || lo > hi {
			return nil, fmt.Errorf("out of range [%d-%d]: %q", min, max, part)
		}
		for i := lo; i <= hi; i += step {
			out[i] = true
		}
	}
	return out, nil
}

// userLocation returns the *time.Location associated with state.User.Timezone,
// falling back to UTC when the timezone is empty or unparsable. Pure helper
// so cron parsing and pretty-printing share one source of truth.
func userLocation(state State) *time.Location {
	tz := strings.TrimSpace(state.User.Timezone)
	if tz == "" {
		return time.UTC
	}
	if loc, err := time.LoadLocation(tz); err == nil {
		return loc
	}
	return time.UTC
}
