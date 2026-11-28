package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/routines"
	"github.com/ironpark/mako/internal/transport"
)

// Operations of a RoutineAction.
const (
	routineOpAdd     = "add"
	routineOpUpdate  = "update"
	routineOpRemove  = "remove"
	routineOpEnable  = "enable"
	routineOpDisable = "disable"
	routineOpRun     = "run"
)

// routineRunTimeout bounds a front-end's run action: the session's start,
// which executeRoutine bounds itself, and the history written after.
const routineRunTimeout = time.Minute

func (d *Daemon) handleListRoutines(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeRoutines, func(r *protocol.ListRoutines) (*protocol.Routines, error) {
		list, err := d.loadRoutines()
		if err != nil {
			return nil, err
		}
		return d.routinesReply(list, r.GetRuns()), nil
	})
}

func (d *Daemon) handleRoutineAction(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeRoutines, func(r *protocol.RoutineAction) (*protocol.Routines, error) {
		return d.actOnRoutine(ctx, r)
	})
}

// actOnRoutine does what a RoutineAction asks of this node's routines, each
// change read, made and written under the runner's lock, so that two
// front-ends' changes never undo each other's.
func (d *Daemon) actOnRoutine(ctx context.Context, r *protocol.RoutineAction) (*protocol.Routines, error) {
	if d.dir == "" {
		return nil, errors.New("this daemon has no configuration directory to keep routines in")
	}
	var (
		list    []routines.Routine
		session *protocol.SessionInfo
		err     error
	)
	switch op := strings.TrimSpace(r.GetOp()); op {
	case routineOpAdd:
		if r.GetSpec() == nil {
			return nil, errors.New("add needs the routine's spec")
		}
		add := routineFromProto(r.GetSpec())
		add.ID = routines.NewID("rtn")
		if add, err = checkRoutine(add); err != nil {
			return nil, err
		}
		list, err = d.updateRoutines(func(list []routines.Routine) ([]routines.Routine, error) {
			return append(list, add), nil
		})
	case routineOpUpdate:
		if r.GetSpec() == nil {
			return nil, errors.New("update needs the routine's spec")
		}
		next := routineFromProto(r.GetSpec())
		next.ID = cmp.Or(strings.TrimSpace(r.GetRoutine()), strings.TrimSpace(next.ID))
		if next, err = checkRoutine(next); err != nil {
			return nil, err
		}
		list, err = d.updateRoutines(func(list []routines.Routine) ([]routines.Routine, error) {
			i := slices.IndexFunc(list, func(x routines.Routine) bool { return x.ID == next.ID })
			if i < 0 {
				return nil, fmt.Errorf("no routine has the ID %q", next.ID)
			}
			list[i] = next
			return list, nil
		})
	case routineOpRemove:
		list, err = d.updateRoutines(func(list []routines.Routine) ([]routines.Routine, error) {
			i, err := findRoutine(list, r.GetRoutine())
			if err != nil {
				return nil, err
			}
			return slices.Delete(list, i, i+1), nil
		})
	case routineOpEnable, routineOpDisable:
		list, err = d.updateRoutines(func(list []routines.Routine) ([]routines.Routine, error) {
			i, err := findRoutine(list, r.GetRoutine())
			if err != nil {
				return nil, err
			}
			list[i].Enabled = op == routineOpEnable
			return list, nil
		})
	case routineOpRun:
		if list, err = d.loadRoutines(); err != nil {
			return nil, err
		}
		i, err := findRoutine(list, r.GetRoutine())
		if err != nil {
			return nil, err
		}
		runCtx, cancel := context.WithTimeout(ctx, routineRunTimeout)
		defer cancel()
		if session, err = d.executeRoutine(runCtx, list[i], "manual", ""); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown routine action %q (expected add, update, remove, enable, disable or run)", op)
	}
	if err != nil {
		return nil, err
	}
	reply := d.routinesReply(list, r.GetRuns())
	reply.Session = session
	return reply, nil
}

// checkRoutine normalizes a routine a front-end sent, and refuses one that
// is not valid or whose project is not a path on this node.
func checkRoutine(r routines.Routine) (routines.Routine, error) {
	r = r.Normalized()
	if err := r.Validate(); err != nil {
		return r, err
	}
	if !filepath.IsAbs(r.Project) {
		return r, fmt.Errorf("routine project %q is not an absolute path", r.Project)
	}
	r.Project = filepath.Clean(r.Project)
	return r, nil
}

// findRoutine is the index of the routine idOrName names.
func findRoutine(list []routines.Routine, idOrName string) (int, error) {
	if strings.TrimSpace(idOrName) == "" {
		return -1, errors.New("no routine named")
	}
	i, ok := routines.Find(list, idOrName)
	if !ok {
		return -1, fmt.Errorf("no routine has the ID or the name %q, or more than one has that name", idOrName)
	}
	return i, nil
}

// routinesReply is list and the latest runs, at most limit of them (0 for
// all), as a Routines reply. Runs that cannot be read are said to be so,
// rather than failing the reply.
func (d *Daemon) routinesReply(list []routines.Routine, limit uint32) *protocol.Routines {
	reply := &protocol.Routines{Routines: make([]*protocol.Routine, len(list))}
	for i, r := range list {
		reply.Routines[i] = routineProto(r)
	}
	runs, err := d.routineRuns()
	if err != nil {
		reply.RunsError = err.Error()
	}
	if limit > 0 && len(runs) > int(limit) {
		runs = runs[:limit]
	}
	reply.Runs = make([]*protocol.RoutineRun, len(runs))
	for i, rec := range runs {
		reply.Runs[i] = &protocol.RoutineRun{
			Id: rec.ID, RoutineId: rec.RoutineID, RoutineName: rec.RoutineName, Trigger: rec.Trigger,
			SessionId: rec.SessionID, Status: rec.Status, Error: rec.Error, Started: protocol.Time(time.Unix(rec.StartedAt, 0)),
		}
	}
	return reply
}

func routineProto(r routines.Routine) *protocol.Routine {
	return &protocol.Routine{
		Id: r.ID, Name: r.Name, Enabled: r.Enabled, Trigger: string(r.Trigger), Schedule: r.Schedule,
		Project: r.Project, Agent: r.Agent, Worktree: r.Worktree, BranchPrefix: r.BranchPrefix, Prompt: r.Prompt,
	}
}

func routineFromProto(p *protocol.Routine) routines.Routine {
	return routines.Routine{
		ID: p.GetId(), Name: p.GetName(), Enabled: p.GetEnabled(), Trigger: routines.TriggerKind(p.GetTrigger()),
		Schedule: p.GetSchedule(), Project: p.GetProject(), Agent: p.GetAgent(), Worktree: p.GetWorktree(),
		BranchPrefix: p.GetBranchPrefix(), Prompt: p.GetPrompt(),
	}
}
