package routines

import (
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
