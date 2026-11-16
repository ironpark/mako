package gui

import (
	"maps"
	"testing"
	"time"
)

// A title a session had is kept on disk and given to it while it has none,
// until its program clears it, or it is a month old.
func TestTitleCache(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	now := time.Now()
	c := loadTitleCache(now)
	c.merge(map[string]string{"a": "Fix the build", "b": "Docs"}, nil, []string{"a", "b"}, now)
	c.merge(map[string]string{"a": "Fix the build", "b": "Docs!"}, nil, []string{"a", "b"}, now.Add(time.Second))
	c.writer.wait()
	if got := loadTitleCache(now).known["b"].Title; got != "Docs" {
		t.Fatalf("written again at once: %q", got)
	}
	c.flush(now.Add(time.Second)) // as the window does as it closes
	c.writer.wait()
	if got := loadTitleCache(now).known["b"].Title; got != "Docs!" {
		t.Fatalf("a change not written on closing: %q", got)
	}

	later := loadTitleCache(now.Add(time.Minute))
	got := later.merge(map[string]string{}, map[string]bool{"b": true}, []string{"a", "b", "c"}, now.Add(time.Minute))
	if !maps.Equal(got, map[string]string{"a": "Fix the build"}) {
		t.Fatalf("titles %v", got)
	}
	later.flush(now.Add(time.Minute))
	later.writer.wait()
	if _, kept := loadTitleCache(now).known["b"]; kept {
		t.Fatal("a title its program cleared was kept")
	}
	if len(loadTitleCache(now.Add(31*24*time.Hour)).known) != 0 {
		t.Fatal("a month-old title was kept")
	}
}
