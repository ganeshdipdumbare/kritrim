package trigger

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron is a parsed five-field cron expression: minute, hour, day of month,
// month, day of week. Fields take *, numbers, ranges (1-5), lists (1,15),
// steps (*/10, 0-30/5) and English names (jan-dec, sun-sat; 7 is also
// Sunday). The descriptors @yearly, @annually, @monthly, @weekly, @daily,
// @midnight and @hourly are accepted. As in Vixie cron, when both day
// fields are restricted a day matching either one fires.
type Cron struct {
	minute, hour, dom, month, dow uint64
	domStar, dowStar              bool
	expr                          string
}

var cronDescriptors = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

var monthNames = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}

var dayNames = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}

// ParseCron parses a cron expression.
func ParseCron(expr string) (Cron, error) {
	spec := strings.TrimSpace(expr)
	if d, ok := cronDescriptors[strings.ToLower(spec)]; ok {
		spec = d
	}
	f := strings.Fields(spec)
	if len(f) != 5 {
		return Cron{}, fmt.Errorf("trigger: cron %q needs 5 fields (minute hour day month weekday)", expr)
	}
	c := Cron{expr: expr}
	var err error
	if c.minute, err = cronField(f[0], 0, 59, nil); err != nil {
		return Cron{}, fmt.Errorf("trigger: cron %q minute: %w", expr, err)
	}
	if c.hour, err = cronField(f[1], 0, 23, nil); err != nil {
		return Cron{}, fmt.Errorf("trigger: cron %q hour: %w", expr, err)
	}
	if c.dom, err = cronField(f[2], 1, 31, nil); err != nil {
		return Cron{}, fmt.Errorf("trigger: cron %q day of month: %w", expr, err)
	}
	if c.month, err = cronField(f[3], 1, 12, monthNames); err != nil {
		return Cron{}, fmt.Errorf("trigger: cron %q month: %w", expr, err)
	}
	if c.dow, err = cronField(f[4], 0, 7, dayNames); err != nil {
		return Cron{}, fmt.Errorf("trigger: cron %q day of week: %w", expr, err)
	}
	if c.dow&(1<<7) != 0 {
		c.dow |= 1
		c.dow &^= 1 << 7
	}
	c.domStar = strings.HasPrefix(f[2], "*")
	c.dowStar = strings.HasPrefix(f[4], "*")
	return c, nil
}

// String returns the expression as given.
func (c Cron) String() string { return c.expr }

func cronField(s string, lo, hi int, names map[string]int) (uint64, error) {
	var bits uint64
	for _, part := range strings.Split(s, ",") {
		rng, step := part, 1
		if i := strings.IndexByte(part, '/'); i >= 0 {
			n, err := strconv.Atoi(part[i+1:])
			if err != nil || n <= 0 {
				return 0, fmt.Errorf("bad step in %q", part)
			}
			rng, step = part[:i], n
		}
		var a, b int
		switch {
		case rng == "*":
			a, b = lo, hi
		case strings.Contains(rng, "-"):
			x, y, _ := strings.Cut(rng, "-")
			var err error
			if a, err = cronValue(x, names); err != nil {
				return 0, err
			}
			if b, err = cronValue(y, names); err != nil {
				return 0, err
			}
		default:
			v, err := cronValue(rng, names)
			if err != nil {
				return 0, err
			}
			a, b = v, v
			if step > 1 {
				b = hi
			}
		}
		if a < lo || b > hi || a > b {
			return 0, fmt.Errorf("%q is outside %d-%d", part, lo, hi)
		}
		for v := a; v <= b; v += step {
			bits |= 1 << uint(v)
		}
	}
	return bits, nil
}

func cronValue(s string, names map[string]int) (int, error) {
	if v, ok := names[strings.ToLower(s)]; ok {
		return v, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("bad value %q", s)
	}
	return v, nil
}

// Next returns the first time after t that matches, in t's location. It
// returns the zero time when nothing matches within five years (for
// example February 30).
func (c Cron) Next(t time.Time) time.Time {
	loc := t.Location()
	t = t.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		if c.month&(1<<uint(t.Month())) == 0 {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, loc)
			continue
		}
		if !c.dayMatches(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc)
			continue
		}
		if c.hour&(1<<uint(t.Hour())) == 0 {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, loc)
			continue
		}
		if c.minute&(1<<uint(t.Minute())) == 0 {
			t = t.Add(time.Minute)
			continue
		}
		return t
	}
	return time.Time{}
}

func (c Cron) dayMatches(t time.Time) bool {
	dom := c.dom&(1<<uint(t.Day())) != 0
	dow := c.dow&(1<<uint(t.Weekday())) != 0
	if c.domStar || c.dowStar {
		return dom && dow
	}
	return dom || dow
}
