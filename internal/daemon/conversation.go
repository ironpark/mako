package daemon

import (
	"context"
	"crypto/rand"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ironpark/sanbo/internal/mcp"
	"github.com/ironpark/sanbo/internal/protocol"
	"github.com/ironpark/sanbo/internal/session"
	"github.com/ironpark/sanbo/internal/transcript"
)

func nativeAgent(agent string, command []string) bool {
	return len(command) > 0 && transcript.Supported(agent) && mcp.Kind(command[0]) == agent
}

// Claude's flags that pick which conversation a launch continues: those
// taking a value, then those that do not.
var (
	claudeSelectorValues = []string{"--session-id", "--resume", "-r", "--from-pr"}
	claudeSelectorFlags  = []string{"--continue", "-c", "--fork-session"}
)

func conversationUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// Only newer Codex versions use a shared app-server. Keep the writer in this
// process tree so a session can be bound without guessing by cwd or timestamp.
var codexNoDaemon sync.Map

func isolatedCodex(ctx context.Context, command []string) []string {
	if value, ok := codexNoDaemon.Load(command[0]); ok {
		if value.(bool) {
			return append(command, "--no-daemon")
		}
		return command
	}
	check, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	help, err := exec.CommandContext(check, command[0], "--help").Output()
	supported := err == nil && strings.Contains(string(help), "--no-daemon")
	codexNoDaemon.Store(command[0], supported)
	if supported {
		return append(command, "--no-daemon")
	}
	return command
}

// resumeCommand preserves runtime settings but removes the original prompt and
// old resume/fork selectors. Replaying the original prompt starts work twice.
func resumeCommand(agent string, command []string, id string) []string {
	values := map[string]bool{}
	flags := "--model -m --config -c --profile -p --sandbox -s --ask-for-approval -a --cd -C --add-dir --enable --disable --local-provider --remote --remote-auth-token-env"
	if agent == "claude" {
		flags = "--model --effort --settings --setting-sources --system-prompt --system-prompt-file --append-system-prompt --append-system-prompt-file --permission-mode --permission-prompts --permission-prompt-tool --plugin-dir --plugin-url --agent --agents --name -n --debug-file --input-format --output-format --json-schema --max-budget-usd --max-turns --fallback-model --system-prompt-snapshot --remote-control-session-name-prefix"
	}
	for _, f := range strings.Fields(flags) {
		values[f] = true
	}
	variadic := map[string]bool{}
	if agent == "claude" {
		for _, f := range strings.Fields("--add-dir --allowedTools --allowed-tools --disallowedTools --disallowed-tools --tools --mcp-config --betas --file") {
			variadic[f] = true
		}
	}
	args := []string{command[0]}
	if agent == "codex" {
		args = append(args, "resume", id)
	} else {
		args = append(args, "--resume", id)
	}
	for i := 1; i < len(command); i++ {
		arg := command[i]
		name, _, equals := strings.Cut(arg, "=")
		if arg == "--" {
			break
		}
		if agent == "codex" && (name == "--image" || name == "-i") {
			if !equals {
				for i+1 < len(command) && !strings.HasPrefix(command[i+1], "-") {
					i++
				}
			}
			continue // startup attachments, like the prompt, already belong to history
		}

		if agent == "claude" && slices.Contains(claudeSelectorValues, name) {
			if !equals && i+1 < len(command) && !strings.HasPrefix(command[i+1], "-") {
				i++
			}
			continue
		}
		if agent == "claude" && slices.Contains(claudeSelectorFlags, name) {
			continue
		}
		if agent == "codex" && (name == "--last" || name == "--all" || name == "--fork") {
			continue
		}
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		args = append(args, arg)
		if !equals && values[name] && i+1 < len(command) {
			i++
			args = append(args, command[i])
		}
		if !equals && variadic[name] {
			for i+1 < len(command) && !strings.HasPrefix(command[i+1], "-") {
				i++
				args = append(args, command[i])
			}
		}
	}
	return args
}

// trackConversation runs for the PTY lifetime, including while the GUI is
// closed or in Terminal mode. It also notices /new and /resume in the agent.
func (d *Daemon) trackConversation(s *session.Session) {
	if !transcript.Supported(s.Info().Agent) {
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.Done():
			return
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
		info := s.Info()
		id, path, err := transcript.ProcessLog(ctx, s.PID(), info.Agent, info.Project)
		if id == "" {
			// No writer found: keep the bound log while it is still the same
			// conversation, and only search the tree when it is not.
			if bound := s.TranscriptPath(); bound != "" && transcript.Verify(info, bound) == nil {
				id, path = info.ConversationID, bound
			} else if info.ConversationID != "" {
				path, err = transcript.Locate(ctx, info, s.TranscriptEnv())
				if path != "" {
					id = info.ConversationID
				}
			}
		}
		cancel()
		if err == nil && id != "" && (id != info.ConversationID || path != s.TranscriptPath()) {
			if err := d.bindConversation(s, id, path); err != nil {
				d.log.Warn("save conversation", "session", s.ID(), "err", err)
			}
		}
		select {
		case <-s.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *Daemon) bindConversation(s *session.Session, id, path string) error {
	d.sessionMu.Lock()
	defer d.sessionMu.Unlock()
	live, ok := d.sessions.get(s.ID())
	if !ok || live != s {
		return nil
	}
	previous, ok := d.saved[s.ID()]
	if !ok {
		return nil
	}
	next := previous
	next.Info.ConversationID = id
	next.TranscriptPath = path
	d.saved[s.ID()] = next
	if err := d.saveSessionsLocked(); err != nil {
		d.saved[s.ID()] = previous
		return err
	}
	s.SetConversation(id, path)
	return nil
}

// chatForSession always reads the attached process's conversation. A client
// cannot silently select unrelated history while typing into this PTY.
func (d *Daemon) chatForSession(ctx context.Context, s *session.Session) (protocol.Chat, error) {
	info := s.Info()
	if path := s.TranscriptPath(); path != "" {
		return transcript.ReadBound(info, path)
	}
	return transcript.Read(ctx, info, s.TranscriptEnv())
}

func hasConversationSelector(command []string) bool {
	for _, arg := range command[1:] {
		name, _, _ := strings.Cut(arg, "=")
		if slices.Contains(claudeSelectorValues, name) || slices.Contains(claudeSelectorFlags, name) {
			return true
		}
	}
	return false
}
