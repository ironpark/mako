package routines

import (
	"bytes"
	"os"
	"testing"
	"time"
)

func TestParseAndMatchCron(t *testing.T) {
	s, err := ParseSchedule("30 9 * * 1-5")
	if err != nil {
		t.Fatal(err)
	}
	// Monday 2026-11-30 09:30
	mon := time.Date(2026, 11, 30, 9, 30, 0, 0, time.UTC)
	if !s.Matches(mon) {
		t.Fatalf("expected %v to match", mon)
	}
	// Sunday 2026-11-29 09:30
	sun := time.Date(2026, 11, 29, 9, 30, 0, 0, time.UTC)
	if s.Matches(sun) {
		t.Fatalf("expected Sunday %v not to match 1-5", sun)
	}
	next := s.Next(sun)
	if !next.Equal(mon) {
		t.Fatalf("Next(%v) = %v, want %v", sun, next, mon)
	}

	step, err := ParseSchedule("*/15 * * * *")
	if err != nil {
		t.Fatal(err)
	}
	if !step.Matches(time.Date(2026, 11, 28, 10, 45, 0, 0, time.UTC)) {
		t.Fatal("expected :45 to match */15")
	}
	if step.Matches(time.Date(2026, 11, 28, 10, 46, 0, 0, time.UTC)) {
		t.Fatal("expected :46 not to match */15")
	}

	ev, err := ParseSchedule("@every 30m")
	if err != nil {
		t.Fatal(err)
	}
	if !ev.IsInterval() || ev.Interval() != 30*time.Minute {
		t.Fatalf("unexpected interval: %+v", ev)
	}
	base := time.Date(2026, 11, 28, 10, 5, 0, 0, time.UTC)
	if got, want := ev.Next(base), time.Date(2026, 11, 28, 10, 30, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("Next(@every 30m) = %v, want %v", got, want)
	}

	if _, err := ParseSchedule("@every 10s"); err == nil {
		t.Fatal("expected sub-minute @every to fail")
	}
	if _, err := ParseSchedule("99 * * * *"); err == nil {
		t.Fatal("expected out-of-range minute to fail")
	}
}

func TestStoreSaveLoadAndHistory(t *testing.T) {
	dir := t.TempDir()
	r := Routine{
		Name:     "Morning tests",
		Enabled:  true,
		Trigger:  TriggerCron,
		Schedule: "0 9 * * 1-5",
		Project:  "/tmp/repo",
		Agent:    "claude",
		Worktree: true,
		Prompt:   "Run go test ./... and summarize failures.",
	}
	if err := Save(dir, []Routine{r}); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil || len(loaded) != 1 || loaded[0].ID == "" || loaded[0].Name != "Morning tests" {
		t.Fatalf("loaded = %+v, err=%v", loaded, err)
	}
	if idx, ok := Find(loaded, "morning tests"); !ok || idx != 0 {
		t.Fatalf("Find by case-insensitive name failed: %d %v", idx, ok)
	}

	if err := AppendHistory(dir, RunRecord{RoutineID: loaded[0].ID, RoutineName: loaded[0].Name, Trigger: "manual", Status: "started", StartedAt: 100}); err != nil {
		t.Fatal(err)
	}
	hist, err := LoadHistory(dir)
	if err != nil || len(hist) != 1 || hist[0].Status != "started" {
		t.Fatalf("history = %+v, err=%v", hist, err)
	}
}

func TestEveryMustBeWholeMinutes(t *testing.T) {
	for _, expr := range []string{"@every 90s", "@every 1m30s", "@every 61s"} {
		if _, err := ParseSchedule(expr); err == nil {
			t.Errorf("ParseSchedule(%q) took a duration that is not whole minutes", expr)
		}
	}
	s, err := ParseSchedule("@every 2m")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	if !s.Matches(base) || s.Matches(base.Add(time.Minute)) {
		t.Fatal("@every 2m should match even minutes only")
	}
	if got, want := s.Next(base), base.Add(2*time.Minute); !got.Equal(want) {
		t.Fatalf("Next(@every 2m) = %v, want %v", got, want)
	}
	r := Routine{Name: "x", Project: "/p", Agent: "claude", Prompt: "p", Trigger: TriggerCron, Schedule: "@every 90s"}
	if err := r.Validate(); err == nil {
		t.Fatal("Validate took @every 90s")
	}
}

// Both day fields restricted: a day matches either, as Vixie cron has it.
// Either one "*": a day matches both.
func TestDayOfMonthAndWeekFollowVixieCron(t *testing.T) {
	// 2026-12-01 is a Tuesday, 2026-12-07 a Monday, 2026-12-08 a Tuesday.
	tue1 := time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)
	mon7 := time.Date(2026, 12, 7, 9, 0, 0, 0, time.UTC)
	tue8 := time.Date(2026, 12, 8, 9, 0, 0, 0, time.UTC)

	or, err := ParseSchedule("0 9 1 * 1")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		at   time.Time
		want bool
	}{{tue1, true}, {mon7, true}, {tue8, false}} {
		if got := or.Matches(c.at); got != c.want {
			t.Errorf("\"0 9 1 * 1\".Matches(%s) = %v, want %v", c.at.Format("Mon Jan 2"), got, c.want)
		}
	}
	if got := or.Next(tue1); !got.Equal(mon7) {
		t.Errorf("Next after the 1st = %v, want Monday the 7th", got)
	}

	domOnly, err := ParseSchedule("0 9 1 * *")
	if err != nil {
		t.Fatal(err)
	}
	if !domOnly.Matches(tue1) || domOnly.Matches(mon7) {
		t.Error(`"0 9 1 * *" should match the 1st only`)
	}
	dowOnly, err := ParseSchedule("0 9 * * 1")
	if err != nil {
		t.Fatal(err)
	}
	if dowOnly.Matches(tue1) || !dowOnly.Matches(mon7) {
		t.Error(`"0 9 * * 1" should match Mondays only`)
	}
	// A stepped "*" field still counts as "*": both must match.
	stepped, err := ParseSchedule("0 9 */2 * 1")
	if err != nil {
		t.Fatal(err)
	}
	if !stepped.Matches(mon7) || stepped.Matches(tue1) || stepped.Matches(time.Date(2026, 12, 14, 9, 0, 0, 0, time.UTC)) {
		t.Error(`"0 9 */2 * 1" should match Mondays on odd days only`)
	}
}

func TestBranchPrefixIsValidated(t *testing.T) {
	for _, ok := range []string{"", "routine/nightly", "bot/fix-ci", "a.b/c_d"} {
		if err := ValidBranchPrefix(ok); err != nil {
			t.Errorf("ValidBranchPrefix(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"-x", "a b", "a..b", "a~b", "a^b", "a:b", "a?b", "a*b", "a[b", `a\b`, "/a", "a/", "a//b", "a/.b", "a.lock/b", "a@{b", "a\tb", "a\x7fb"} {
		if err := ValidBranchPrefix(bad); err == nil {
			t.Errorf("ValidBranchPrefix(%q) took it", bad)
		}
	}
	r := Routine{Name: "x", Project: "/p", Agent: "claude", Prompt: "p", Trigger: TriggerCron, Schedule: "@daily", BranchPrefix: "-rf"}
	if err := r.Validate(); err == nil {
		t.Fatal("Validate took a branch prefix beginning with -")
	}
}

// A history that cannot be read is never replaced by the record appended.
func TestAppendHistoryKeepsAnUnreadableHistory(t *testing.T) {
	dir := t.TempDir()
	bad := []byte("[{\"id\": \"run-1\", \"status\": \"started\"}, oops")
	if err := os.WriteFile(HistoryPath(dir), bad, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AppendHistory(dir, RunRecord{RoutineID: "r", Status: "started"}); err == nil {
		t.Fatal("AppendHistory wrote over a history it could not read")
	}
	got, err := os.ReadFile(HistoryPath(dir))
	if err != nil || !bytes.Equal(got, bad) {
		t.Fatalf("history = %q, %v; want it untouched", got, err)
	}
}

func TestFindRefusesAnAmbiguousName(t *testing.T) {
	list := []Routine{{ID: "rtn-a", Name: "Nightly"}, {ID: "rtn-b", Name: "nightly"}}
	if _, ok := Find(list, "NIGHTLY"); ok {
		t.Fatal("Find chose one of two routines of the same name")
	}
	if i, ok := Find(list, "rtn-b"); !ok || i != 1 {
		t.Fatalf("Find by ID = %d, %v", i, ok)
	}
}
