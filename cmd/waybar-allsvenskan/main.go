// waybar-allsvenskan: a waybar custom-module daemon that surfaces Allsvenskan
// fixtures and live scores via FotMob's unofficial JSON endpoint.
//
// Pill (label): the most relevant match for the configured team —
//   live > scheduled today > finished today > yesterday's result > next upcoming.
// Tooltip: every Allsvenskan match across yesterday, today, and the next 5 days
//   so the user can glance at the wider league state without leaving the bar.
//
// FotMob has no public API. The endpoint we call powers their own web app, so
// it can change without notice. Keep the User-Agent realistic and don't hammer.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

const (
	leagueID  = 67 // FotMob's id for Allsvenskan
	apiURL    = "https://www.fotmob.com/api/data/matches"
	userAgent = "Mozilla/5.0 (waybar-allsvenskan)"
	soccerGlyph = "" // nf-fa-futbol_o
)

type teamFlag []string

func (t *teamFlag) String() string     { return strings.Join(*t, ",") }
func (t *teamFlag) Set(s string) error { *t = append(*t, s); return nil }

type Team struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Score *int   `json:"score"`
}

type LiveTime struct {
	Short string `json:"short"`
	Long  string `json:"long"`
}

type Status struct {
	UTCTime   string    `json:"utcTime"`
	Started   bool      `json:"started"`
	Finished  bool      `json:"finished"`
	Cancelled bool      `json:"cancelled"`
	Ongoing   bool      `json:"ongoing"`
	ScoreStr  string    `json:"scoreStr"`
	LiveTime  *LiveTime `json:"liveTime,omitempty"`
}

type Match struct {
	ID     int    `json:"id"`
	Time   string `json:"time"`
	Home   Team   `json:"home"`
	Away   Team   `json:"away"`
	Status Status `json:"status"`
	TimeTS int64  `json:"timeTS"`
}

type LeagueData struct {
	ID      int     `json:"id"`
	Name    string  `json:"name"`
	Matches []Match `json:"matches"`
}

type APIResp struct {
	Date    string       `json:"date"`
	Leagues []LeagueData `json:"leagues"`
}

type WaybarOut struct {
	Text    string `json:"text"`
	Tooltip string `json:"tooltip,omitempty"`
	Class   string `json:"class,omitempty"`
}

// cachedDay memoises a fetched day's matches. Keys are YYYYMMDD strings.
//
// FotMob is an unofficial API — to stay polite (and avoid IP throttling) we
// cache by date with TTLs based on how likely the data is to change:
//   - past: 24h (matches rarely get retroactively edited)
//   - today: 30s (live scores need fresh data)
//   - future: 1h (fixtures occasionally shift)
type cachedDay struct {
	matches []Match
	fetched time.Time
}

var dayCache = map[string]cachedDay{}

// todayInMatchWindow is true if any cached match for today is in its active
// window: 15 min before scheduled kickoff through 150 min after (covers
// warmup, 90 min + injury time, and post-match settling). Outside this
// window the data won't change in real time, so we can poll lazily.
//
// Falls back to "true" if we have no cache yet — better to poll once and
// learn than miss the start of a match.
func todayInMatchWindow(now time.Time) bool {
	c, ok := dayCache[now.Format("20060102")]
	if !ok {
		return true
	}
	for _, m := range c.matches {
		t, err := time.Parse("02.01.2006 15:04", m.Time)
		if err != nil {
			continue
		}
		if now.After(t.Add(-15*time.Minute)) && now.Before(t.Add(150*time.Minute)) {
			return true
		}
	}
	return false
}

func ttlFor(d, now time.Time) time.Duration {
	switch {
	case sameDay(d, now):
		if todayInMatchWindow(now) {
			return 30 * time.Second
		}
		return 10 * time.Minute
	case d.Before(now):
		return 24 * time.Hour
	default:
		return 1 * time.Hour
	}
}

func fetchDayCached(d time.Time) ([]Match, error) {
	key := d.Format("20060102")
	now := time.Now()
	if c, ok := dayCache[key]; ok && time.Since(c.fetched) < ttlFor(d, now) {
		return c.matches, nil
	}
	ms, err := fetchDay(d)
	if err != nil {
		// Serve stale data on transient API errors instead of disappearing.
		if c, ok := dayCache[key]; ok {
			return c.matches, nil
		}
		return nil, err
	}
	dayCache[key] = cachedDay{matches: ms, fetched: now}
	return ms, nil
}

func fetchDay(t time.Time) ([]Match, error) {
	url := fmt.Sprintf("%s?date=%s&ccode3=SWE&timezone=Europe/Stockholm", apiURL, t.Format("20060102"))
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var r APIResp
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	for _, l := range r.Leagues {
		if l.ID == leagueID {
			return l.Matches, nil
		}
	}
	return nil, nil
}

func score(t Team) int {
	if t.Score == nil {
		return 0
	}
	return *t.Score
}

func includesTeam(m Match, teams []string) bool {
	for _, t := range teams {
		if strings.EqualFold(m.Home.Name, t) || strings.EqualFold(m.Away.Name, t) {
			return true
		}
	}
	return false
}

func filterTeam(matches []Match, teams []string) []Match {
	var out []Match
	for _, m := range matches {
		if includesTeam(m, teams) {
			out = append(out, m)
		}
	}
	return out
}

func formatPill(m Match) string {
	switch {
	case m.Status.Ongoing && m.Status.LiveTime != nil:
		return fmt.Sprintf("%s %s %d-%d %s · %s", soccerGlyph,
			m.Home.Name, score(m.Home), score(m.Away), m.Away.Name, m.Status.LiveTime.Short)
	case m.Status.Finished:
		return fmt.Sprintf("%s %s %d-%d %s · FT", soccerGlyph,
			m.Home.Name, score(m.Home), score(m.Away), m.Away.Name)
	default:
		t, err := time.Parse("02.01.2006 15:04", m.Time)
		if err != nil {
			return fmt.Sprintf("%s %s vs %s", soccerGlyph, m.Home.Name, m.Away.Name)
		}
		return fmt.Sprintf("%s %s vs %s · %s", soccerGlyph,
			m.Home.Name, m.Away.Name, friendlyTime(t))
	}
}

// friendlyTime renders a kickoff time relative to "now":
// today  -> "19:00"
// tomorrow -> "tomorrow 19:00"
// within a week -> "Sun 19:00"
// further -> "Sun 03 May 19:00"
func friendlyTime(t time.Time) string {
	now := time.Now()
	t = t.Local()
	switch {
	case sameDay(t, now):
		return t.Format("15:04")
	case sameDay(t, now.AddDate(0, 0, 1)):
		return "tomorrow " + t.Format("15:04")
	case t.Sub(now) < 7*24*time.Hour:
		return t.Format("Mon 15:04")
	default:
		return t.Format("Mon 02 Jan 15:04")
	}
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

func formatTooltipLine(m Match) string {
	t, _ := time.Parse("02.01.2006 15:04", m.Time)
	timeStr := t.Local().Format("15:04")
	switch {
	case m.Status.Ongoing && m.Status.LiveTime != nil:
		return fmt.Sprintf("%s  %s %d–%d %s · %s",
			timeStr, m.Home.Name, score(m.Home), score(m.Away), m.Away.Name, m.Status.LiveTime.Short)
	case m.Status.Finished:
		return fmt.Sprintf("%s  %s %d–%d %s · FT",
			timeStr, m.Home.Name, score(m.Home), score(m.Away), m.Away.Name)
	default:
		return fmt.Sprintf("%s  %s vs %s",
			timeStr, m.Home.Name, m.Away.Name)
	}
}

type dayBucket struct {
	label   string
	date    time.Time
	matches []Match
}

func buildBuckets(now time.Time, daysBefore, daysAfter int) []dayBucket {
	var out []dayBucket
	for off := -daysBefore; off <= daysAfter; off++ {
		d := now.AddDate(0, 0, off)
		var label string
		switch off {
		case -1:
			label = "Yesterday"
		case 0:
			label = "Today"
		case 1:
			label = "Tomorrow"
		default:
			label = d.Format("Mon 02 Jan")
		}
		out = append(out, dayBucket{label: label, date: d})
	}
	return out
}

func buildTooltip(buckets []dayBucket) string {
	var b strings.Builder
	for _, bk := range buckets {
		if len(bk.matches) == 0 {
			continue
		}
		ms := append([]Match(nil), bk.matches...)
		sort.Slice(ms, func(i, j int) bool { return ms[i].TimeTS < ms[j].TimeTS })
		fmt.Fprintf(&b, "<b>%s</b>\n", bk.label)
		for _, m := range ms {
			fmt.Fprintf(&b, "  %s\n", formatTooltipLine(m))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// pickPrimary scans buckets in priority order and returns the most relevant
// match for the configured team(s):
//   1. live ongoing today
//   2. scheduled today
//   3. finished today (visible until end of today)
//   4. next upcoming (any future bucket)
//   5. yesterday (only if nothing upcoming in the window)
func pickPrimary(buckets []dayBucket, teams []string) *Match {
	var todayLive, todayScheduled, todayFinished, yesterday, upcoming []Match
	for _, bk := range buckets {
		ms := filterTeam(bk.matches, teams)
		switch bk.label {
		case "Today":
			for _, m := range ms {
				switch {
				case m.Status.Ongoing:
					todayLive = append(todayLive, m)
				case m.Status.Finished:
					todayFinished = append(todayFinished, m)
				default:
					todayScheduled = append(todayScheduled, m)
				}
			}
		case "Yesterday":
			yesterday = append(yesterday, ms...)
		default:
			// Future days
			off := int(bk.date.Sub(time.Now()).Hours() / 24)
			if off >= 1 {
				upcoming = append(upcoming, ms...)
			}
		}
	}
	pick := func(ms []Match) *Match {
		if len(ms) == 0 {
			return nil
		}
		sort.Slice(ms, func(i, j int) bool { return ms[i].TimeTS < ms[j].TimeTS })
		return &ms[0]
	}
	for _, group := range [][]Match{todayLive, todayScheduled, todayFinished, upcoming, yesterday} {
		if m := pick(group); m != nil {
			return m
		}
	}
	return nil
}

// matchState captures the per-match facts we care about for transition
// detection between polling cycles.
type matchState struct {
	started   bool
	finished  bool
	atHalfTime bool
	homeScore int
	awayScore int
}

// prevStates is keyed by FotMob match ID. Lives for the lifetime of the
// daemon — restart loses state and you'll miss any transitions that happened
// during the gap, which is acceptable.
var prevStates = map[int]matchState{}

func snapshot(m Match) matchState {
	return matchState{
		started:    m.Status.Started,
		finished:   m.Status.Finished,
		atHalfTime: m.Status.LiveTime != nil && m.Status.LiveTime.Short == "HT",
		homeScore:  score(m.Home),
		awayScore:  score(m.Away),
	}
}

func notifySend(title, body string) {
	// notify-send is best-effort; if it's not installed (or no D-Bus) we just
	// drop the notification rather than failing the whole daemon.
	cmd := exec.Command("notify-send",
		"--app-name", "Allsvenskan",
		"--icon", "applications-games",
		title, body)
	_ = cmd.Run()
}

// emitTransitions diffs current matches against prevStates and fires
// notify-send for kickoff, goal, half-time, and full-time events.
// teams filters which matches we care about (empty = all).
func emitTransitions(matches []Match, teams []string) {
	for _, m := range matches {
		if len(teams) > 0 && !includesTeam(m, teams) {
			continue
		}
		cur := snapshot(m)
		prev, hadPrev := prevStates[m.ID]
		prevStates[m.ID] = cur
		if !hadPrev {
			// First sighting — nothing to compare. Avoids spurious "kick off"
			// notifications when the daemon starts mid-match.
			continue
		}
		scoreLine := fmt.Sprintf("%s %d–%d %s",
			m.Home.Name, cur.homeScore, cur.awayScore, m.Away.Name)

		if !prev.started && cur.started {
			notifySend(" Kick-off",
				fmt.Sprintf("%s vs %s", m.Home.Name, m.Away.Name))
		}
		if cur.homeScore != prev.homeScore || cur.awayScore != prev.awayScore {
			scorer := m.Home.Name
			if cur.awayScore > prev.awayScore {
				scorer = m.Away.Name
			}
			notifySend(" Goal!",
				fmt.Sprintf("%s\n%s scored", scoreLine, scorer))
		}
		if !prev.atHalfTime && cur.atHalfTime {
			notifySend(" Half-time", scoreLine)
		}
		if !prev.finished && cur.finished {
			notifySend(" Full-time", scoreLine)
		}
	}
}

func emit(teams []string, daysBefore, daysAfter int, notify, notifyAll bool) {
	now := time.Now()
	buckets := buildBuckets(now, daysBefore, daysAfter)
	for i := range buckets {
		ms, err := fetchDayCached(buckets[i].date)
		if err == nil {
			buckets[i].matches = ms
		}
	}

	if notify {
		// Today's matches are the only ones that can transition between polls;
		// past/future days don't change state in real time.
		for _, bk := range buckets {
			if bk.label != "Today" {
				continue
			}
			notifyTeams := teams
			if notifyAll {
				notifyTeams = nil
			}
			emitTransitions(bk.matches, notifyTeams)
		}
	}

	primary := pickPrimary(buckets, teams)
	out := WaybarOut{}
	if primary == nil {
		// No matches in window — emit empty so waybar collapses the module.
		fmt.Println(`{"text":""}`)
		return
	}

	out.Text = formatPill(*primary)
	out.Tooltip = buildTooltip(buckets)

	// If any Allsvenskan match is live right now, prefix a red dot + count so
	// the user knows the league is in action even when their own team isn't
	// playing. Skip when the pill itself is already showing a live score.
	if !primary.Status.Ongoing {
		live := 0
		for _, bk := range buckets {
			for _, m := range bk.matches {
				if m.Status.Ongoing {
					live++
				}
			}
		}
		if live > 0 {
			out.Text = fmt.Sprintf(
				"<span foreground='#ed8796'>●</span> %d live · %s", live, out.Text)
		}
	}

	switch {
	case primary.Status.Ongoing:
		out.Class = "live"
	case primary.Status.Finished:
		out.Class = "finished"
	default:
		out.Class = "scheduled"
	}
	enc, _ := json.Marshal(out)
	fmt.Println(string(enc))
}

func main() {
	var teams teamFlag
	flag.Var(&teams, "team", "Team to follow (repeatable). Match is by exact name (case-insensitive).")
	interval := flag.Int("interval", 60, "Poll interval in seconds")
	daysBefore := flag.Int("days-before", 1, "How many past days to include in the tooltip")
	daysAfter := flag.Int("days-after", 7, "How many future days to include in the tooltip")
	notify := flag.Bool("notify", true, "Emit notify-send for kickoff/goal/HT/FT events")
	notifyAll := flag.Bool("notify-all", false, "Notify for every Allsvenskan match, not just --team(s)")
	flag.Parse()

	if len(teams) == 0 {
		fmt.Fprintln(os.Stderr, "at least one --team flag is required")
		os.Exit(1)
	}

	for {
		emit(teams, *daysBefore, *daysAfter, *notify, *notifyAll)
		time.Sleep(time.Duration(*interval) * time.Second)
	}
}
