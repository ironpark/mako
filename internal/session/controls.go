package session

import (
	"errors"
	"fmt"
	"slices"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/protocol"
)

// What of an agent a front-end may set, at its prompt, as the user would:
// its permission mode, the key that moves it on pressed until the screen
// names the mode; its model and effort, by its own commands.

// controls are what of the agent called name a front-end may set, nil for
// nothing.
func controls(name string) *protocol.AgentControls {
	var c protocol.AgentControls
	if m, ok := agents.As[agent.Modes](name); ok {
		c.Modes = m.Modes()
	}
	if m, ok := agents.As[agent.Models](name); ok {
		c.Models, c.Efforts = m.Models(), m.Efforts()
	}
	if len(c.Modes)+len(c.Models)+len(c.Efforts) == 0 {
		return nil
	}
	return &c
}

// Configure sets what of mode, model and effort is given.
func (s *Session) Configure(mode, model, effort string) error {
	c := controls(s.spec.Agent)
	if mode != "" {
		if !slices.Contains(c.GetModes(), mode) {
			return fmt.Errorf("the agent takes no mode %q", mode)
		}
		if err := s.setMode(mode); err != nil {
			return err
		}
	}
	m, _ := agents.As[agent.Models](s.spec.Agent)
	set := func(value string, offered []string, command func(string) string) error {
		if value == "" {
			return nil
		}
		if !slices.Contains(offered, value) {
			return fmt.Errorf("the agent takes no %q", value)
		}
		return s.Send(command(value), []byte{'\r'})
	}
	if model != "" {
		if err := set(model, c.GetModels(), m.ModelCommand); err != nil {
			return err
		}
	}
	if effort != "" {
		return set(effort, c.GetEfforts(), m.EffortCommand)
	}
	return nil
}

// setMode presses the agent's mode key, back at its prompt, until its
// screen names mode, once round its modes at most.
func (s *Session) setMode(mode string) error {
	m, _ := agents.As[agent.Modes](s.spec.Agent)
	s.recall()
	for range len(m.Modes()) + 2 {
		screen, seen, ok := s.recallScreen()
		if !ok {
			return errors.New("session: exited")
		}
		switch m.ScreenMode(screen) {
		case mode:
			return nil
		case "":
			return errors.New("the agent shows no mode now")
		}
		if _, err := s.Write(m.ModeKey()); err != nil {
			return err
		}
		if !s.awaitFoot(screen, seen) {
			return errors.New("session: exited")
		}
	}
	return fmt.Errorf("the agent offers no %q mode now", mode)
}
