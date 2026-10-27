// Package agents lists the agents mako knows, each by its driver, in the
// order a launcher shows them.
package agents

import (
	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agent/agy"
	"github.com/ironpark/mako/internal/agent/claude"
	"github.com/ironpark/mako/internal/agent/codex"
	"github.com/ironpark/mako/internal/agent/opencode"
	"github.com/ironpark/mako/internal/agent/pi"
)

var all = []agent.Driver{
	claude.New(),
	codex.New(),
	agy.New(),
	pi.New(),
	pi.NewOMP(),
	pi.NewOMO(),
	opencode.New(),
}

// Entry is a driver with what it says of itself, read once.
type Entry struct {
	Driver agent.Driver
	Info   agent.Info
	Caps   uint32 // protocol's Cap bits
}

// byName and byProgram index the drivers.
var byName, byProgram = func() (map[string]Entry, map[string]Entry) {
	names, programs := map[string]Entry{}, map[string]Entry{}
	for _, d := range all {
		e := Entry{Driver: d, Info: d.Info(), Caps: agent.Caps(d)}
		names[e.Info.Name] = e
		for _, p := range e.Info.Programs {
			programs[p] = e
		}
	}
	return names, programs
}()

// All are the drivers, in order.
func All() []agent.Driver { return all }

// Lookup is the agent called name.
func Lookup(name string) (Entry, bool) {
	e, ok := byName[name]
	return e, ok
}

// As is the driver of the agent called name, when it has the capability T.
func As[T any](name string) (T, bool) {
	t, ok := byName[name].Driver.(T)
	return t, ok
}

// ByProgram is the agent the program runs, by its file name.
func ByProgram(program string) (Entry, bool) {
	e, ok := byProgram[agent.ProgramName(program)]
	return e, ok
}

// Caps are the protocol's Cap bits for the agent called name, 0 for one
// mako does not know.
func Caps(name string) uint32 { return byName[name].Caps }
