package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ironpark/mako/internal/layoutdoc"
	"github.com/ironpark/mako/internal/session"
	"github.com/ironpark/mako/internal/workspace"
)

// The tools an agent reads and changes the window layout documents with
// (docs/agent-mcp.md): named as the server's others are, a verb and its
// object, since an agent's tool names take no dots. A change goes
// through the user as layoutchange.go says.

const (
	toolListBlocks  = "list_blocks"
	toolGetLayout   = "get_layout"
	toolSetLayout   = "set_layout"
	toolPatchLayout = "patch_layout"
	toolUndoLayout  = "undo_layout"
)

// layoutNamePattern is what a document may be named.
var layoutNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type listBlocksResult struct {
	Blocks []layoutdoc.Block `json:"blocks"`
}

type layoutArgs struct {
	Document string `json:"document,omitempty" jsonschema:"The window layout document's name; default when left out, the one every space that names none uses."`
}

type layoutSpace struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Document string `json:"document"`
}

type getLayoutResult struct {
	Document string `json:"document"`
	// Revision is the document's, for if_rev; "" while there is none and
	// the window shows its built-in default, which Doc is then.
	Revision  string         `json:"revision"`
	Doc       map[string]any `json:"doc"`
	Builtin   bool           `json:"builtin,omitempty"`
	Documents []string       `json:"documents"`
	Spaces    []layoutSpace  `json:"spaces"`
}

type setLayoutArgs struct {
	Document string         `json:"document,omitempty" jsonschema:"The document's name; default when left out."`
	Doc      map[string]any `json:"doc" jsonschema:"The whole document, as get_layout returns one: {version, root}."`
	IfRev    string         `json:"if_rev" jsonschema:"The revision get_layout returned; the change is refused if the document changed since. \"\" while there is none."`
}

type patchLayoutArgs struct {
	Document string         `json:"document,omitempty" jsonschema:"The document's name; default when left out."`
	Ops      []layoutdoc.Op `json:"ops" jsonschema:"The changes, by node id, applied in order; the document after is checked whole."`
	IfRev    *string        `json:"if_rev,omitempty" jsonschema:"The revision the ops were made against; refused if the document changed since."`
}

type changeLayoutResult struct {
	Document string `json:"document"`
	Revision string `json:"revision"`
}

// addLayoutTools gives the agent's server the window layout tools.
func (d *Daemon) addLayoutTools(server *sdk.Server, peer *agentPeer) {
	sdk.AddTool(server, &sdk.Tool{Name: toolListBlocks, Description: "List the blocks a window layout document can place, with each one's props as a JSON Schema: the catalog to build a document from. A document's leaves are these blocks and the panes, which hold the session tabs.", Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, _ *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, listBlocksResult, error) {
		if _, err := d.projectCaller(peer); err != nil {
			return nil, listBlocksResult{}, err
		}
		return nil, listBlocksResult{Blocks: d.blocks()}, nil
	})
	sdk.AddTool(server, &sdk.Tool{Name: toolGetLayout, Description: "Read a window layout document of the mako window, shared by the user's nodes, with its revision, and which document each space uses. The document is a tree of row, column and stack nodes whose leaves are blocks (list_blocks) and the panes; every node has an id.", Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, _ *sdk.CallToolRequest, in layoutArgs) (*sdk.CallToolResult, getLayoutResult, error) {
		if _, err := d.projectCaller(peer); err != nil {
			return nil, getLayoutResult{}, err
		}
		out, err := d.getLayout(in.Document)
		return nil, out, err
	})
	sdk.AddTool(server, &sdk.Tool{Name: toolSetLayout, Description: "Replace a window layout document whole. The user is asked to allow it unless they allowed such changes before, sees it applied, and keeps it or has it reverted; without an answer it reverts after 10 seconds. Only a kept document reaches the user's other nodes. Refused when if_rev is not the revision the document has."}, func(ctx context.Context, _ *sdk.CallToolRequest, in setLayoutArgs) (*sdk.CallToolResult, changeLayoutResult, error) {
		s, err := d.projectCaller(peer)
		if err != nil {
			return nil, changeLayoutResult{}, err
		}
		name, err := layoutName(in.Document)
		if err != nil {
			return nil, changeLayoutResult{}, err
		}
		body, err := json.Marshal(in.Doc)
		if err != nil {
			return nil, changeLayoutResult{}, err
		}
		doc, parsed, err := layoutdoc.Canonical(body, d.blockCatalog())
		if err != nil {
			return nil, changeLayoutResult{}, fmt.Errorf("the document cannot be drawn: %w", err)
		}
		if parsed.Version != layoutdoc.Version {
			return nil, changeLayoutResult{}, fmt.Errorf("this node writes documents of version %d", layoutdoc.Version)
		}
		_, rev := d.windowLayout(name)
		if in.IfRev != rev {
			return nil, changeLayoutResult{}, fmt.Errorf("the document's revision is %q, not %q: read it again with get_layout", rev, in.IfRev)
		}
		rev, err = d.changeLayout(ctx, s, layoutChange{tool: toolSetLayout, name: name, doc: doc, base: rev, summary: "replace the whole document"})
		return nil, changeLayoutResult{Document: name, Revision: rev}, err
	})
	sdk.AddTool(server, &sdk.Tool{Name: toolPatchLayout, Description: "Change a window layout document by node id: insert a node into a row, column or stack; remove, move, or resize one (size in px, or grow); or select the block a stack shows. The document after must be one the window can draw, or nothing changes. The user is asked to allow it unless they allowed such changes before; only then does it reach the user's other nodes."}, func(ctx context.Context, _ *sdk.CallToolRequest, in patchLayoutArgs) (*sdk.CallToolResult, changeLayoutResult, error) {
		s, err := d.projectCaller(peer)
		if err != nil {
			return nil, changeLayoutResult{}, err
		}
		name, err := layoutName(in.Document)
		if err != nil {
			return nil, changeLayoutResult{}, err
		}
		cur, rev, _, err := d.layoutNow(name)
		if err != nil {
			return nil, changeLayoutResult{}, err
		}
		if in.IfRev != nil && *in.IfRev != rev {
			return nil, changeLayoutResult{}, fmt.Errorf("the document's revision is %q, not %q: read it again with get_layout", rev, *in.IfRev)
		}
		next, err := layoutdoc.Apply(cur, in.Ops, d.blockCatalog())
		if err != nil {
			return nil, changeLayoutResult{}, err
		}
		doc, err := json.Marshal(next)
		if err != nil {
			return nil, changeLayoutResult{}, err
		}
		rev, err = d.changeLayout(ctx, s, layoutChange{tool: toolPatchLayout, name: name, doc: doc, base: rev, summary: opsSummary(in.Ops)})
		return nil, changeLayoutResult{Document: name, Revision: rev}, err
	})
	sdk.AddTool(server, &sdk.Tool{Name: toolUndoLayout, Description: "Go back to the version of a window layout document before the last change made on this node, as a new change, which the user is asked to allow unless they allowed such changes before. The versions are this node's alone."}, func(ctx context.Context, _ *sdk.CallToolRequest, in layoutArgs) (*sdk.CallToolResult, changeLayoutResult, error) {
		s, err := d.projectCaller(peer)
		if err != nil {
			return nil, changeLayoutResult{}, err
		}
		name, err := layoutName(in.Document)
		if err != nil {
			return nil, changeLayoutResult{}, err
		}
		rev, err := d.undoLayout(ctx, s, name)
		return nil, changeLayoutResult{Document: name, Revision: rev}, err
	})
}

// layoutName is the document a tool names, the default for none.
func layoutName(name string) (string, error) {
	if name == "" {
		return layoutdoc.DefaultName, nil
	}
	if !layoutNamePattern.MatchString(name) {
		return "", errors.New("a document's name is letters, digits and ._-, at most 64")
	}
	return name, nil
}

// layoutNow is the document of the name as the window draws it, its
// revision, and whether it is the built-in default, there being none.
func (d *Daemon) layoutNow(name string) (*layoutdoc.Doc, string, bool, error) {
	body, rev := d.windowLayout(name)
	if body == nil {
		return layoutdoc.Default(), "", true, nil
	}
	doc, err := layoutdoc.Parse(body, d.blockCatalog())
	if err != nil {
		// A window draws the default for it, which a change starts from.
		return layoutdoc.Default(), rev, true, nil
	}
	return doc, rev, false, nil
}

func (d *Daemon) getLayout(name string) (getLayoutResult, error) {
	name, err := layoutName(name)
	if err != nil {
		return getLayoutResult{}, err
	}
	out := getLayoutResult{Document: name, Documents: []string{}, Spaces: []layoutSpace{}}
	body, rev := d.windowLayout(name)
	out.Revision = rev
	if body == nil || json.Unmarshal(body, &out.Doc) != nil {
		body, _ = json.Marshal(layoutdoc.Default())
		_ = json.Unmarshal(body, &out.Doc)
		out.Builtin = true
	}
	st := d.workspace.get(false).GetState()
	for n := range st.GetWindowLayouts() {
		if workspace.WindowLayout(st, n) != nil {
			out.Documents = append(out.Documents, n)
		}
	}
	slices.Sort(out.Documents)
	for _, sp := range workspace.Spaces(st) {
		out.Spaces = append(out.Spaces, layoutSpace{ID: sp.GetId(), Name: sp.GetName().GetValue(), Document: cmpOr(sp.GetLayout().GetValue(), layoutdoc.DefaultName)})
	}
	return out, nil
}

// undoLayout asks to go back to the document's version before this node's
// last change of it, none when there was none.
func (d *Daemon) undoLayout(ctx context.Context, s *session.Session, name string) (string, error) {
	v, ok := d.layouts.last(name)
	if !ok {
		return "", errLayoutNoUndo
	}
	_, rev := d.windowLayout(name)
	return d.changeLayout(ctx, s, layoutChange{tool: toolUndoLayout, name: name, doc: v.doc, base: rev, undo: true, summary: "go back to the version before the last change"})
}

// opsSummary says what a patch does, in a line.
func opsSummary(ops []layoutdoc.Op) string {
	parts := make([]string, 0, len(ops))
	for _, op := range ops {
		switch op.Op {
		case "insert":
			parts = append(parts, "insert into "+op.Parent)
		case "move":
			parts = append(parts, fmt.Sprintf("move %s into %s", op.ID, op.Parent))
		case "select":
			parts = append(parts, "select in "+op.ID)
		default:
			parts = append(parts, op.Op+" "+op.ID)
		}
	}
	return strings.Join(parts, ", ")
}
