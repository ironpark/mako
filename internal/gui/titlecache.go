package gui

import (
	"encoding/json"
	"maps"
	"os"
	"time"
)

// The titles sessions' programs set on their terminals are kept in
// titles.json, so that a tab whose terminal is not attached, and a session
// asleep or restored, which has no title of its own until its program sets
// one again, is called as it was rather than by its agent's name.

// titleKept is how long a title is kept after its session last had it;
// titleRestamp how often one unchanged is noted again as had; and
// titleWrites how often the file is written at most, a program that sets
// its title as it works changing it far more often.
const (
	titleKept    = 30 * 24 * time.Hour
	titleRestamp = 24 * time.Hour
	titleWrites  = 15 * time.Second
)

// cachedTitle is a session's title, and when it last had it.
type cachedTitle struct {
	Title string    `json:"title"`
	At    time.Time `json:"at"`
}

// titleCache is titles.json as the window holds it.
type titleCache struct {
	known   map[string]cachedTitle
	writer  *fileWriter
	dirty   bool      // changed since last written
	written time.Time // when last written
}

// loadTitleCache reads titles.json, leaving out what has been kept too
// long.
func loadTitleCache(now time.Time) *titleCache {
	c := &titleCache{known: map[string]cachedTitle{}, writer: &fileWriter{what: "titles"}}
	if path, err := configFile("titles.json"); err == nil {
		if body, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(body, &c.known)
		}
	}
	maps.DeleteFunc(c.known, func(_ string, t cachedTitle) bool { return t.Title == "" || now.Sub(t.At) > titleKept })
	return c
}

// merge notes the titles the sessions have now, and gives back titles with
// those it kept for the sessions that have none. live are the sessions
// whose terminals are attached, which have cleared a title they have not.
// What changed reaches titles.json once titleWrites has passed since it
// was last written.
func (c *titleCache) merge(titles map[string]string, live map[string]bool, sessions []string, now time.Time) map[string]string {
	for id, title := range titles {
		if k, ok := c.known[id]; !ok || k.Title != title || now.Sub(k.At) > titleRestamp {
			c.known[id] = cachedTitle{title, now}
			c.dirty = true
		}
	}
	for _, id := range sessions {
		if _, ok := titles[id]; ok {
			continue
		}
		if live[id] {
			if _, ok := c.known[id]; ok {
				delete(c.known, id) // its program cleared it
				c.dirty = true
			}
			continue
		}
		if k, ok := c.known[id]; ok {
			titles[id] = k.Title
		}
	}
	if now.Sub(c.written) >= titleWrites {
		c.flush(now)
	}
	return titles
}

// flush writes titles.json, if it has changed since it was last written.
func (c *titleCache) flush(now time.Time) {
	if !c.dirty {
		return
	}
	c.dirty, c.written = false, now
	if path, err := configFile("titles.json"); err == nil {
		if body, err := json.Marshal(c.known); err == nil {
			c.writer.write(path, body)
		}
	}
}
