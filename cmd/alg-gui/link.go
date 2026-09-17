package main

import (
	"errors"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"

	"alg/internal/proto"
)

const requestTimeout = 10 * time.Second

// runOnUI hands fn to the UI goroutine. Everything that touches a widget from
// a worker or timer goroutine goes through it. It is a variable because
// Fyne's test driver has no UI goroutine to hand anything to, so the tests
// substitute their own serialisation.
var runOnUI = fyne.Do

type pendingCall struct {
	req  proto.Request
	out  any
	done func(error)
}

// link owns the connection to the daemon.
//
// Requests are queued to a single worker goroutine and their callbacks are
// marshalled back with fyne.Do, so a slow EC read can never freeze the
// interface. The connection is opened lazily and reopened after a failure,
// which is what lets the window survive a daemon restart.
type link struct {
	calls         chan pendingCall
	session       proto.Session
	statusPending atomic.Bool
}

func newLink() *link {
	l := &link{calls: make(chan pendingCall, 64)}
	go l.run()
	return l
}

func (l *link) run() {
	for c := range l.calls {
		err := l.session.Call(c.req, requestTimeout, c.out)
		if c.done != nil {
			done := c.done
			runOnUI(func() { done(err) })
		}
	}
}

func (l *link) enqueue(req proto.Request, out any, done func(error)) {
	select {
	case l.calls <- pendingCall{req: req, out: out, done: done}:
	default:
		if done != nil {
			// Callers may or may not be on the UI goroutine; always come
			// back the same way a real reply would.
			go runOnUI(func() { done(errors.New("too many requests are waiting on the daemon")) })
		}
	}
}

// send queues a request whose reply carries nothing the caller needs.
func (l *link) send(req proto.Request, done func(error)) {
	l.enqueue(req, nil, done)
}

// fetch queues a request and hands the decoded reply to done.
func fetch[T any](l *link, req proto.Request, done func(*T, error)) {
	out := new(T)
	l.enqueue(req, out, func(err error) { done(out, err) })
}

// status polls are dropped while one is still in flight, so a stalled daemon
// cannot build up a backlog of them.
func (l *link) status(done func(*proto.Status, error)) {
	if !l.statusPending.CompareAndSwap(false, true) {
		return
	}
	fetch(l, proto.Request{Op: proto.OpStatus}, func(st *proto.Status, err error) {
		l.statusPending.Store(false)
		done(st, err)
	})
}
