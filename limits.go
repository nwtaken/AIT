package main

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // the reset message names its zone ("Europe/London")
)

func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	json.Unmarshal(raw, &parts)
	var out []string
	for _, p := range parts {
		if p.Type == "text" {
			out = append(out, p.Text)
		}
	}
	return strings.Join(out, " ")
}

var (
	resetRe = regexp.MustCompile(`(?i)resets\s+(?:(?:on\s+)?([a-z]{3,9})\.?\s+(\d{1,2}),?\s+(?:at\s+)?)?(\d{1,2})(?::(\d{2}))?\s*(am|pm)(?:\s*\(([^)]+)\))?`)
	months  = map[string]time.Month{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
)

// resetTime turns "resets 12:30am (Europe/London)" or "resets Oct 3, 5pm"
// into an instant. Unparseable text gets a short cooldown: trying an account
// that is still limited only costs one failed turn, while guessing too long
// strands a usable account.
func resetTime(text string, now time.Time) time.Time {
	m := resetRe.FindStringSubmatch(text)
	if m == nil {
		return now.Add(30 * time.Minute)
	}
	loc := time.Local
	if m[6] != "" {
		if l, err := time.LoadLocation(strings.TrimSpace(m[6])); err == nil {
			loc = l
		}
	}
	hour, _ := strconv.Atoi(m[3])
	minute, _ := strconv.Atoi(m[4])
	if strings.EqualFold(m[5], "pm") && hour != 12 {
		hour += 12
	} else if strings.EqualFold(m[5], "am") && hour == 12 {
		hour = 0
	}
	n := now.In(loc)
	var t time.Time
	if mon, ok := months[strings.ToLower(m[1])[:min(3, len(m[1]))]]; ok && m[1] != "" {
		day, _ := strconv.Atoi(m[2])
		t = time.Date(n.Year(), mon, day, hour, minute, 0, 0, loc)
		if t.Before(n.Add(-24 * time.Hour)) {
			t = t.AddDate(1, 0, 0)
		}
	} else {
		t = time.Date(n.Year(), n.Month(), n.Day(), hour, minute, 0, 0, loc)
		if !t.After(n) {
			t = t.AddDate(0, 0, 1)
		}
	}
	return t.Add(time.Minute) // a little past the boundary, never a hair before
}
