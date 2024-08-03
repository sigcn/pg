package net

import (
	"net"
	"os"
	"sync"
	"time"
)

type deadlineError struct{}

func (deadlineError) Error() string   { return "deadline exceeded" }
func (deadlineError) Temporary() bool { return true }
func (deadlineError) Timeout() bool   { return true }
func (deadlineError) Unwrap() error   { return os.ErrDeadlineExceeded }

var ErrDeadline net.Error = &deadlineError{}

type Deadline struct {
	init               sync.Once
	deadline           chan struct{}
	deadlineTimerMutex sync.Mutex
	deadlineTimer      *time.Timer
}

func (d *Deadline) SetDeadline(t time.Time) {
	d.init.Do(func() {
		d.deadline = make(chan struct{})
	})
	d.deadlineTimerMutex.Lock()
	defer d.deadlineTimerMutex.Unlock()
	if d.deadlineTimer != nil {
		d.deadlineTimer.Stop()
		d.deadlineTimer = nil
	}
	if t.IsZero() {
		return
	}
	timeout := time.Until(t)
	if timeout > 0 {
		d.deadlineTimer = time.AfterFunc(timeout, func() { d.deadline <- struct{}{} })
		return
	}
	d.deadline <- struct{}{}
}

func (d *Deadline) Deadline() <-chan struct{} {
	d.init.Do(func() {
		d.deadline = make(chan struct{})
	})
	return d.deadline
}
