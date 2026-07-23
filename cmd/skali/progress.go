package main

import (
	"github.com/Hinkolas/skali/internal/clirender"
)

// taskProgress renders engine stages through the live task printer; an
// unconcluded stage on error settles as failed via Abort.
type taskProgress struct {
	tasks   *clirender.Tasks
	current *clirender.Task
}

func newTaskProgress(tasks *clirender.Tasks) *taskProgress {
	return &taskProgress{tasks: tasks}
}

func (p *taskProgress) Start(title string) {
	if p.current != nil {
		p.current.Done("")
	}
	p.current = p.tasks.Start(title)
}

func (p *taskProgress) Done(detail string) {
	if p.current == nil {
		return
	}
	p.current.Done(detail)
	p.current = nil
}

func (p *taskProgress) Skip(detail string) {
	if p.current == nil {
		return
	}
	p.current.Skip(detail)
	p.current = nil
}

// Note publishes a transient status line under the running stage, used to
// surface what a long wait is blocked on. It is a no-op with no stage
// running.
func (p *taskProgress) Note(line string) {
	if p.current == nil {
		return
	}
	p.current.Note(line)
}

func (p *taskProgress) Abort() {
	if p.current == nil {
		return
	}
	p.current.Fail()
	p.current = nil
}
