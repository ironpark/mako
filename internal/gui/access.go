package gui

import (
	"context"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/notify"
	"github.com/ironpark/mako/internal/protocol"
)

// Access: who this node's daemon lets in, the nodes asking to be, and
// asking another node to let this one in. Trust runs one way: a node
// that lets this one in can be driven from here, and one this node lets
// in can drive it.

// changeAccess sends the daemon an access change, and shows how it went
// on the Access page: done, or the error.
func (m *model) changeAccess(req *protocol.Access, done string) {
	m.answerAccess(req, done, nil)
}

// answerAccess is changeAccess, telling failed of an error as well, for
// an answer given away from the Access page.
func (m *model) answerAccess(req *protocol.Access, done string, failed func(error)) {
	m.AccessNote.Set("")
	ggui.Async(func() (*protocol.AccessInfo, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		c, err := m.client(ctx)
		if err != nil {
			return nil, err
		}
		return c.Access(ctx, req)
	}, func(info *protocol.AccessInfo, err error) {
		if err != nil {
			m.AccessNote.Set(err.Error())
			if failed != nil {
				failed(err)
			}
			return
		}
		m.Access.Set(info)
		m.AccessNote.Set(done)
	})
}

// noteRequests tells the user of each request new since the last poll,
// as a toast and, unless turned off, on the desktop. The toast stays until
// the request is answered, and lets the node in from its button; one
// answered elsewhere takes its toast with it. UI goroutine.
func (m *model) noteRequests(info *protocol.AccessInfo) {
	waiting := map[string]ui.ToastID{}
	for _, r := range info.GetRequests() {
		id := r.GetNode().GetId() + " " + r.GetKey().GetFingerprint()
		if toast, ok := m.asked[id]; ok {
			waiting[id] = toast
			continue
		}
		node := r.GetNode()
		body := Tf("%s asks to be let in. Settings ▸ Access answers it.", node.GetName())
		ask := Tf("%s asks to be let in. Allow it here, or answer in Settings ▸ Access.", node.GetName())
		waiting[id] = m.toaster.Push(ui.Toast(T("Access requested"), ask).Duration(0).Action(T("Allow"), func() {
			m.answerAccess(&protocol.Access{Op: "allow", NodeId: node.GetId()}, Tf("%s is let in.", node.GetName()), func(err error) {
				m.toaster.Push(ui.Toast(T("Could not change access"), err.Error()).Destructive())
			})
		}))
		if ggui.Peek(m.Prefs).Notify {
			go notify.Post(T("Access requested"), body, settingsTarget("access"))
		}
	}
	for id, toast := range m.asked {
		if _, ok := waiting[id]; !ok {
			m.toaster.Dismiss(toast)
		}
	}
	m.asked = waiting
}

func accessSettings(m *model) ggui.Widget {
	return page(T("Access"), T("Who this node lets in. A node it lets in can start programs and read and write files here; trust runs one way, so that node must let this one in for the reverse."),
		ggui.View(m.AccessNote, func(s string) ggui.Widget {
			if s == "" {
				return ggui.Box()
			}
			return ggui.Text(s).Size(12.5).Color(tokAccentBright)
		}),
		ggui.View(m.Access, func(info *protocol.AccessInfo) ggui.Widget {
			if info == nil {
				return muted(ggui.Text(T("The daemon is not on libp2p, or cannot be reached.")).Size(13))
			}
			return ggui.Column(
				section(T("Requests"), requestList(m, info.GetRequests())),
				section(T("This node"), card(
					keyRow(m, T("Node key"), T("Another node lets this one in by this key, or by the SSH key below."), info.GetKey()),
					ownerRow(m, info.GetOwner()),
				)),
				section(T("Let in"), keyList(m, info.GetKeys())),
			).Gap(28).Stretch()
		}),
		section(T("SSH key"), card(
			stackedRow(T("Vouch with an SSH key"), T("The public key of an SSH key of yours, such as ~/.ssh/id_ed25519.pub, its private half in the SSH agent or beside it without a passphrase. Nodes that let the key in let this node in, and the nodes it vouches for let each other in."),
				ui.TextField(hostField(m, func(d *hostDraft) *string { return &d.SSHKey })).Placeholder("~/.ssh/id_ed25519.pub").Name(T("SSH key"))),
		)),
		saveBar(m),
	)
}

// keyRow shows a key with a button that copies its line, for another
// node's authorized_keys.
func keyRow(m *model, title, hint string, key *protocol.AccessKey) ggui.Widget {
	return row(title, hint, ggui.Row(
		ggui.Text(key.GetFingerprint()).Size(12).Color(tokMuted).Selectable().NoWrap().Ellipsis(),
		copyButton(m, key.GetLine()),
	).Gap(8))
}

func ownerRow(m *model, owner *protocol.AccessKey) ggui.Widget {
	if owner == nil {
		return row(T("Vouched for by"), T("No SSH key vouches for this node."), ggui.Box())
	}
	return keyRow(m, T("Vouched for by"), T("The SSH key that vouches for this node."), owner)
}

// requestList shows the nodes asking to be let in, each with its answers.
func requestList(m *model, requests []*protocol.AccessRequest) ggui.Widget {
	if len(requests) == 0 {
		return muted(ggui.Text(T("No node is asking to be let in.")).Size(13))
	}
	rows := make([]ggui.Widget, 0, len(requests))
	for _, r := range requests {
		node := r.GetNode()
		detail := Tf("key %s", r.GetKey().GetFingerprint())
		if r.GetOwner() != nil {
			detail += " · " + Tf("SSH key %s", r.GetOwner().GetFingerprint())
		}
		text := []ggui.Widget{
			ggui.Text(node.GetName()).Size(13.5).Weight(ggui.WeightMedium),
			ggui.Text(detail).Size(11.5).Color(tokExtraMuted).Selectable(),
		}
		if r.GetNote() != "" {
			text = append(text, muted(ggui.Text("“"+r.GetNote()+"”").Size(12.5)))
		}
		buttons := []ggui.Widget{
			ui.Button(T("Allow"), func() {
				m.changeAccess(&protocol.Access{Op: "allow", NodeId: node.GetId()}, Tf("%s is let in.", node.GetName()))
			}),
		}
		if r.GetOwner() != nil {
			buttons = append(buttons, ui.Button(T("Allow the SSH key"), func() {
				m.changeAccess(&protocol.Access{Op: "allow", NodeId: node.GetId(), Owner: true}, Tf("%s's SSH key is let in, with every node it vouches for.", node.GetName()))
			}).Outline())
		}
		buttons = append(buttons, ui.Button(T("Deny"), func() {
			m.changeAccess(&protocol.Access{Op: "deny", NodeId: node.GetId()}, "")
		}).Outline())
		rows = append(rows, ggui.Box(ggui.Row(
			ggui.Expanded(ggui.Column(text...).Gap(3).Stretch()),
			ggui.Row(buttons...).Gap(6),
		).Gap(16)).Pad(14, 16))
	}
	return card(rows...)
}

// keyList shows the keys let in, each with a button that takes it out,
// and a field to let one in.
func keyList(m *model, keys []*protocol.AccessKey) ggui.Widget {
	var rows []ggui.Widget
	for _, k := range keys {
		name := k.GetComment()
		if name == "" {
			name = k.GetType()
		}
		if k.GetSource() != "" {
			// A key of the user's own SSH file is taken out there.
			rows = append(rows, row(name, k.GetFingerprint()+" · "+Tf("from %s", k.GetSource()), ggui.Box()))
			continue
		}
		rows = append(rows, row(name, k.GetFingerprint(), ui.Button(T("Revoke"), func() {
			m.changeAccess(&protocol.Access{Op: "revoke", Key: k.GetFingerprint()}, Tf("%s is let out.", name))
		}).Outline()))
	}
	if len(rows) == 0 {
		rows = append(rows, row(T("No key is let in yet."), T("Allow a request above, or add a key below."), ggui.Box()))
	}
	add := stackedRow(T("Let a key in"), T("A public key as authorized_keys has it: another node's key, which it shows under Settings ▸ Access, or an SSH key."),
		ggui.Row(
			ggui.Expanded(ui.TextField(m.AccessKey).Placeholder("ssh-ed25519 AAAA… name").Name(T("Let a key in"))),
			ui.Button(T("Add"), func() {
				line := ggui.Peek(m.AccessKey)
				m.AccessKey.Set("")
				m.changeAccess(&protocol.Access{Op: "add", Key: line}, T("The key is let in."))
			}).BindDisabled(ggui.Map(m.AccessKey, func(s string) bool { return s == "" })),
		).Gap(8))
	return card(append(rows, add)...)
}

// requestAccess asks node to let this one in.
func (m *model) requestAccess(node *protocol.Node) {
	m.changeAccess(&protocol.Access{Op: "request", NodeId: node.GetId()}, Tf("Asked %s to let this node in.", node.GetName()))
}

// reachable reports whether the window can drive node: this daemon, a
// node that lets it in, or one reached through nodes that do.
func reachable(n *protocol.Node) bool {
	s := n.Standing()
	return s == protocol.StandingSelf || s == protocol.StandingBoth || s == protocol.StandingUs || s == protocol.StandingVia
}
