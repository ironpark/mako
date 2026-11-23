package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ironpark/mako/internal/prompttmpl"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
)

// The tool an agent saves a prompt template with (docs/agent-mcp.md), as
// the built-in //template asks it to. A template is the window's: kept in
// the templates folder beside the window's config.json and filled in
// there, whichever node the session is on. So the daemon only checks it,
// as Settings ▸ Templates would, and asks for it as an approval on the
// agent's session, which the windows of every node that lets this one in
// show; the window the user allows it in saves it in its own folder, and
// says so by allowing with protocol.TemplateSaved. Whether a template of
// the name is there to replace is that window's to see.

const toolSaveTemplate = "save_template"

// templateAskFor is how long a template waits for the user before it is
// refused.
const templateAskFor = 10 * time.Minute

// maxTemplate is how long a template's file may be, in bytes; its whole
// text rides on the approval.
const maxTemplate = 32 << 10

// templateNamePattern is what an agent may name a template: a subset of
// what a file may be named, so that it stays in the folder and reads the
// same on every platform.
var templateNamePattern = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N}._-]{0,63}$`)

var (
	errTemplateNoWindow = errors.New("no window of mako is open to show the user the template; nothing was saved")
	errTemplateDeclined = errors.New("the user declined to save the template")
	errTemplateNotSaved = errors.New("the user allowed the template in a mako that cannot save templates; nothing was saved")
)

type saveTemplateArgs struct {
	Name    string `json:"name" jsonschema:"The template's name, called as //name: letters, digits, . _ and -, beginning with a letter or digit, at most 64."`
	Content string `json:"content" jsonschema:"The template's whole file: YAML front matter between --- lines, then the prompt."`
	Replace bool   `json:"replace,omitempty" jsonschema:"Replace the user's template of the name, or stand in for the built-in one; without it a name taken is refused."`
}

type saveTemplateResult struct {
	Name string `json:"name"`
	// Call is how the user calls it: //name and its arguments.
	Call string `json:"call"`
}

// templateInput is what the approval holds for the window to save.
type templateInput struct {
	Name    string `json:"name"`
	Content string `json:"content"`
	Replace bool   `json:"replace,omitempty"`
}

// templateAsks are the templates waiting for the user, by approval ID.
type templateAsks struct {
	mu      sync.Mutex
	waiting map[string]chan templateAnswer
}

// templateAnswer is the user's, as AnswerApproval gives it.
type templateAnswer struct{ decision, option, message string }

func (a *templateAsks) open(id string) <-chan templateAnswer {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.waiting == nil {
		a.waiting = map[string]chan templateAnswer{}
	}
	ch := make(chan templateAnswer, 1)
	a.waiting[id] = ch
	return ch
}

func (a *templateAsks) close(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.waiting, id)
}

// owns reports whether the approval id is a template's.
func (a *templateAsks) owns(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.waiting[id]
	return ok
}

// answer gives the template waiting as id the user's answer; the first
// answer stands.
func (a *templateAsks) answer(id string, ans templateAnswer) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	ch, ok := a.waiting[id]
	if !ok {
		return fmt.Errorf("no template waits as %q", id)
	}
	select {
	case ch <- ans:
	default:
	}
	return nil
}

// addTemplateTools gives the agent's server save_template.
func (d *Daemon) addTemplateTools(server *sdk.Server, peer *agentPeer) {
	sdk.AddTool(server, &sdk.Tool{Name: toolSaveTemplate, Description: "Save a mako prompt template you wrote among the user's, called in Chat as //name. The content is checked as mako reads templates first; an error says what to fix, so fix it and call again. The user is then shown the template in the mako window and saves it there or declines, which returns an error; it is refused when no window is open. A name the user already has a template of, or a built-in template's, is refused unless replace is set, and the user is shown the change. Returns how to call it."}, func(ctx context.Context, _ *sdk.CallToolRequest, in saveTemplateArgs) (*sdk.CallToolResult, saveTemplateResult, error) {
		s, err := d.projectCaller(peer)
		if err != nil {
			return nil, saveTemplateResult{}, err
		}
		out, err := d.saveTemplate(ctx, s, in)
		return nil, out, err
	})
}

// checkTemplate finds what is wrong with a template an agent asks to
// save, as Settings ▸ Templates would, and returns it read.
func checkTemplate(in saveTemplateArgs) (*prompttmpl.Template, error) {
	if !templateNamePattern.MatchString(in.Name) || !prompttmpl.ValidName(in.Name) {
		return nil, fmt.Errorf("the name %q is not one a template takes: letters, digits, . _ and -, beginning with a letter or digit, at most 64", in.Name)
	}
	if len(in.Content) > maxTemplate {
		return nil, fmt.Errorf("the template is %d bytes; one is at most %d", len(in.Content), maxTemplate)
	}
	if prompttmpl.BuiltinTemplate(in.Name) != nil && !in.Replace {
		return nil, fmt.Errorf("//%s is a built-in template; choose another name, or set replace to stand in for it", in.Name)
	}
	t, err := prompttmpl.Parse(in.Name, []byte(in.Content))
	if err != nil {
		return nil, fmt.Errorf("the template does not read: %w", err)
	}
	return t, nil
}

// templateWindowOpen reports whether a mako window is connected to show and
// save a template. Any local window watching layouts counts even when
// "Approve in the window" is off for terminal tool hooks.
func (d *Daemon) templateWindowOpen() bool {
	return d.approvals.watching() || d.layouts.watched()
}

// saveTemplate asks the user to save the template the agent of session s
// wrote, as an approval on its session, and waits for a window to save it
// or the user to decline it.
func (d *Daemon) saveTemplate(ctx context.Context, s *session.Session, in saveTemplateArgs) (saveTemplateResult, error) {
	t, err := checkTemplate(in)
	if err != nil {
		return saveTemplateResult{}, err
	}
	if !d.templateWindowOpen() {
		return saveTemplateResult{}, errTemplateNoWindow
	}
	id := approvalID()
	answers := d.templateAsks.open(id)
	defer d.templateAsks.close(id)
	input, _ := json.Marshal(templateInput(in))
	call := "//" + t.Name
	if u := t.Usage(); u != "" {
		call += " " + u
	}
	seq, _, ok := s.HoldApproval(&protocol.Approval{
		Id: id, Tool: toolSaveTemplate, Detail: call, Input: string(input), Since: timestamppb.Now(), Kind: protocol.ToolTemplate,
	})
	if !ok {
		return saveTemplateResult{}, errors.New("the session cannot ask the user now")
	}
	defer s.FinishApproval(seq, id, protocol.AgentWorking)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	give := time.After(templateAskFor)
	for {
		select {
		case a := <-answers:
			switch {
			case a.decision == protocol.DecisionAllow && a.option == protocol.TemplateSaved:
				return saveTemplateResult{Name: t.Name, Call: call}, nil
			case a.decision == protocol.DecisionAllow:
				return saveTemplateResult{}, errTemplateNotSaved
			case a.message != "":
				return saveTemplateResult{}, fmt.Errorf("%w: %s", errTemplateDeclined, a.message)
			}
			return saveTemplateResult{}, errTemplateDeclined
		case <-tick.C:
			if !d.templateWindowOpen() {
				return saveTemplateResult{}, errTemplateNoWindow
			}
		case <-give:
			return saveTemplateResult{}, fmt.Errorf("the user did not answer within %v; nothing was saved", templateAskFor)
		case <-s.Done():
			return saveTemplateResult{}, errors.New("the session ended")
		case <-ctx.Done():
			return saveTemplateResult{}, ctx.Err()
		}
	}
}
