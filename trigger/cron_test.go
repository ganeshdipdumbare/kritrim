package trigger

import (
	"testing"
	"time"
)

func TestCronNext(t *testing.T) {
	utc := time.UTC
	base := time.Date(2026, 11, 14, 10, 7, 30, 0, utc) // a Saturday
	cases := []struct {
		expr string
		want time.Time
	}{
		{"* * * * *", time.Date(2026, 11, 14, 10, 8, 0, 0, utc)},
		{"*/15 * * * *", time.Date(2026, 11, 14, 10, 15, 0, 0, utc)},
		{"0 9 * * mon-fri", time.Date(2026, 11, 16, 9, 0, 0, 0, utc)},
		{"30 8 1 * *", time.Date(2026, 12, 1, 8, 30, 0, 0, utc)},
		{"0 0 * jan *", time.Date(2027, 1, 1, 0, 0, 0, 0, utc)},
		{"@daily", time.Date(2026, 11, 15, 0, 0, 0, 0, utc)},
		{"@hourly", time.Date(2026, 11, 14, 11, 0, 0, 0, utc)},
		{"0 12 * * 7", time.Date(2026, 11, 15, 12, 0, 0, 0, utc)}, // 7 is Sunday
		{"5,10 10 * * *", time.Date(2026, 11, 14, 10, 10, 0, 0, utc)},
		{"0-30/10 11 * * *", time.Date(2026, 11, 14, 11, 0, 0, 0, utc)},
		// Both day fields restricted: either matches (the 20th, or a Monday).
		{"0 0 20 * mon", time.Date(2026, 11, 16, 0, 0, 0, 0, utc)},
		{"0 0 29 2 *", time.Date(2028, 2, 29, 0, 0, 0, 0, utc)},
	}
	for _, c := range cases {
		cr, err := ParseCron(c.expr)
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		if got := cr.Next(base); !got.Equal(c.want) {
			t.Errorf("%s: next = %s, want %s", c.expr, got, c.want)
		}
	}
}

func TestCronNever(t *testing.T) {
	cr, err := ParseCron("0 0 30 2 *")
	if err != nil {
		t.Fatal(err)
	}
	if got := cr.Next(time.Now()); !got.IsZero() {
		t.Fatalf("February 30 fired at %s", got)
	}
}

func TestCronLocation(t *testing.T) {
	loc := time.FixedZone("UTC+5", 5*3600)
	cr, _ := ParseCron("0 9 * * *")
	got := cr.Next(time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC).In(loc))
	if got.Hour() != 9 || got.Location() != loc || !got.Equal(time.Date(2026, 11, 14, 4, 0, 0, 0, time.UTC)) {
		t.Fatalf("got %s", got)
	}
}

func TestCronRejects(t *testing.T) {
	for _, expr := range []string{"", "* * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *",
		"* * * * 8", "*/0 * * * *", "5-1 * * * *", "x * * * *", "@sometimes"} {
		if _, err := ParseCron(expr); err == nil {
			t.Errorf("%q accepted", expr)
		}
	}
}

func FuzzCron(f *testing.F) {
	for _, s := range []string{"* * * * *", "*/5 1-3 1,15 jan-mar mon", "@weekly"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, expr string) {
		c, err := ParseCron(expr)
		if err != nil {
			return
		}
		base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		if n := c.Next(base); !n.IsZero() && !n.After(base) {
			t.Fatalf("%q: next %s not after %s", expr, n, base)
		}
	})
}

func BenchmarkCronNext(b *testing.B) {
	c, _ := ParseCron("*/15 9-17 * * mon-fri")
	t := time.Date(2026, 11, 14, 10, 7, 0, 0, time.UTC)
	b.ReportAllocs()
	for b.Loop() {
		t = c.Next(t)
	}
}
