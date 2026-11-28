package session

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/protocol"
	"google.golang.org/protobuf/proto"
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
	if _, ok := agents.As[agent.FastMode](name); ok {
		c.FastMode = true
	}
	if len(c.Modes)+len(c.Models)+len(c.Efforts) == 0 && !c.FastMode {
		return nil
	}
	return &c
}

// controlsLocked returns the session's active AgentControls, including
// dynamic model catalogs and native bridge capabilities such as Codex Fast
// Mode while running.
func (s *Session) controlsLocked() *protocol.AgentControls {
	c := controls(s.spec.Agent)
	if s.state == protocol.StateExited {
		return c
	}
	if len(c.GetModels()) == 0 && len(s.dynamicModels) > 0 {
		if c == nil {
			c = &protocol.AgentControls{}
		}
		c.Models = slices.Clone(s.dynamicModels)
	}
	if s.fastModeSupported {
		if c == nil {
			c = &protocol.AgentControls{}
		}
		c.FastMode = true
	}
	return c
}

func (s *Session) controls() *protocol.AgentControls {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.controlsLocked()
}

// SetDynamicModels records dynamically discovered model IDs (e.g. from
// agent.ModelCatalog) for agents whose static Models() list is empty,
// notifying listeners when the list changes.
func (s *Session) SetDynamicModels(models []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == protocol.StateExited || slices.Equal(s.dynamicModels, models) {
		return
	}
	s.dynamicModels = slices.Clone(models)
	s.changedLocked()
}

// SetDynamicModelVariants records additional valid variant IDs (such as
// "<base>-<tier>" slugs from a ModelCatalog) accepted by Configure without
// cluttering SessionInfo.Controls.Models.
func (s *Session) SetDynamicModelVariants(variants []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == protocol.StateExited {
		return
	}
	if len(variants) == 0 {
		s.dynamicModelVariants = nil
		return
	}
	m := make(map[string]bool, len(variants))
	for _, v := range variants {
		if v != "" {
			m[v] = true
		}
	}
	s.dynamicModelVariants = m
}

func (s *Session) hasDynamicModelVariant(model string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state != protocol.StateExited && s.dynamicModelVariants[model]
}

// SetFastModeSupported records whether this session supports toggling fast
// mode via ConfigureAgent and notifies listeners when capability changes.
func (s *Session) SetFastModeSupported(supported bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == protocol.StateExited || s.fastModeSupported == supported {
		return
	}
	s.fastModeSupported = supported
	s.changedLocked()
}

// ReportFastMode records whether fast mode is currently enabled on this
// session and notifies listeners when the state changes.
func (s *Session) ReportFastMode(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == protocol.StateExited || s.fastMode == enabled {
		return
	}
	s.fastMode = enabled
	s.changedLocked()
}

// ConfigureFastMode toggles fast mode on a running session using the agent's
// slash command when its driver implements agent.FastMode.
func (s *Session) ConfigureFastMode(enabled bool) error {
	fm, ok := agents.As[agent.FastMode](s.spec.Agent)
	if !ok {
		return errors.New("the agent does not support fast mode")
	}
	cmd := fm.FastModeCommand(enabled)
	if cmd == "" {
		return errors.New("the agent does not support fast mode")
	}
	if err := s.Send(cmd, []byte{'\r'}); err != nil {
		return err
	}
	if m, ok := agents.As[agent.Models](s.spec.Agent); ok {
		if err := s.confirm(m); err != nil {
			return err
		}
	}
	s.ReportFastMode(enabled)
	s.Report(&protocol.Usage{FastMode: proto.Bool(enabled)})
	return nil
}

// Configure sets what of mode, model and effort is given.
func (s *Session) Configure(mode, model, effort string) error {
	c := s.controls()
	if mode != "" {
		if !slices.Contains(c.GetModes(), mode) {
			return fmt.Errorf("the agent takes no mode %q", mode)
		}
		if err := s.setMode(mode); err != nil {
			return err
		}
	}
	m, _ := agents.As[agent.Models](s.spec.Agent)
	set := func(value string, allowed func(string) bool, command func(agent.Models, string) string) error {
		if value == "" {
			return nil
		}
		if m == nil || !allowed(value) {
			return fmt.Errorf("the agent takes no %q", value)
		}
		if err := s.Send(command(m, value), []byte{'\r'}); err != nil {
			return err
		}
		return s.confirm(m)
	}
	if model != "" {
		offered := c.GetModels()
		if err := set(model, func(v string) bool {
			return slices.Contains(offered, v) || s.hasDynamicModelVariant(v)
		}, agent.Models.ModelCommand); err != nil {
			return err
		}
	}
	if effort != "" {
		offered := c.GetEfforts()
		return set(effort, func(v string) bool {
			return slices.Contains(offered, v)
		}, agent.Models.EffortCommand)
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

// confirmWait is how long the agent is given to ask before it switches.
const confirmWait = 1500 * time.Millisecond

// confirm says yes to what the agent asks before it switches, if it asks
// within a while of the command.
func (s *Session) confirm(m agent.Models) error {
	for deadline := time.Now().Add(confirmWait); time.Now().Before(deadline); time.Sleep(4 * recallPoll) {
		screen, _, ok := s.recallScreen()
		if !ok {
			return errors.New("session: exited")
		}
		if keys := m.ConfirmKeys(screen); keys != nil {
			_, err := s.Write(keys)
			return err
		}
	}
	return nil
}
