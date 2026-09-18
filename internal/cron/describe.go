package cron

import (
	"fmt"
	"strconv"
	"strings"
)

var dayNames = [...]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

// Describe words a five-field expression for people: every N minutes or
// hours, hourly, daily, weekly, or monthly. Anything else comes back as the
// expression itself so the reader still sees the truth. The console words
// schedules the same way (web/src/lib/cron.ts).
func Describe(expr string) string {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return expr
	}
	minute, hour, dom, month, dow := fields[0], fields[1], fields[2], fields[3], fields[4]
	if month != "*" {
		return expr
	}
	number := func(field string) (int, bool) {
		n, err := strconv.Atoi(field)
		return n, err == nil && n >= 0
	}
	every := func(field string) (int, bool) {
		if !strings.HasPrefix(field, "*/") {
			return 0, false
		}
		n, err := strconv.Atoi(field[2:])
		return n, err == nil && n > 0
	}
	clock := func() string {
		h, _ := number(hour)
		m, _ := number(minute)
		return fmt.Sprintf("%02d:%02d", h, m)
	}
	_, minuteFixed := number(minute)
	_, hourFixed := number(hour)
	switch {
	case dom == "*" && dow == "*":
		if minute == "*" && hour == "*" {
			return "every minute"
		}
		if n, ok := every(minute); ok && hour == "*" {
			if n == 1 {
				return "every minute"
			}
			return fmt.Sprintf("every %d minutes", n)
		}
		if n, ok := every(hour); ok && minuteFixed {
			if n == 1 {
				return "hourly"
			}
			return fmt.Sprintf("every %d hours", n)
		}
		if minuteFixed && hour == "*" {
			m, _ := number(minute)
			return fmt.Sprintf("hourly at :%02d", m)
		}
		if minuteFixed && hourFixed {
			return "daily at " + clock()
		}
	case dom == "*" && minuteFixed && hourFixed:
		if d, ok := number(dow); ok && d <= 7 {
			return fmt.Sprintf("weekly on %s at %s", dayNames[d%7], clock())
		}
	case dow == "*" && minuteFixed && hourFixed:
		if d, ok := number(dom); ok && d >= 1 && d <= 31 {
			return fmt.Sprintf("monthly on the %s at %s", ordinal(d), clock())
		}
	}
	return expr
}

func ordinal(n int) string {
	if mod100 := n % 100; mod100 >= 11 && mod100 <= 13 {
		return fmt.Sprintf("%dth", n)
	}
	switch n % 10 {
	case 1:
		return fmt.Sprintf("%dst", n)
	case 2:
		return fmt.Sprintf("%dnd", n)
	case 3:
		return fmt.Sprintf("%drd", n)
	}
	return fmt.Sprintf("%dth", n)
}
