package jsonrpc

import (
	"sync/atomic"

	"golang.org/x/sync/semaphore"
)

// wsLimiter caps how many websocket connections the node accepts and how many
// requests may be in flight at once. A nil limiter, or a zero ceiling, means
// that particular limit is disabled.
type wsLimiter struct {
	maxConns uint64
	conns    atomic.Int64

	perConn uint64

	inFlight *semaphore.Weighted
}

func newWSLimiter(maxConns, maxInFlight, perConn uint64) *wsLimiter {
	l := &wsLimiter{
		maxConns: maxConns,
		perConn:  perConn,
	}

	if maxInFlight > 0 {
		l.inFlight = semaphore.NewWeighted(int64(maxInFlight))
	}

	return l
}

func (l *wsLimiter) tryAddConn() bool {
	if l == nil {
		return true
	}

	if l.maxConns == 0 {
		l.conns.Add(1)

		return true
	}

	for {
		n := l.conns.Load()
		if uint64(n) >= l.maxConns {
			return false
		}

		if l.conns.CompareAndSwap(n, n+1) {
			return true
		}
	}
}

func (l *wsLimiter) removeConn() {
	if l == nil {
		return
	}

	l.conns.Add(-1)
}

func (l *wsLimiter) connCount() int64 {
	if l == nil {
		return 0
	}

	return l.conns.Load()
}

func (l *wsLimiter) tryAcquireInFlight() bool {
	if l == nil || l.inFlight == nil {
		return true
	}

	return l.inFlight.TryAcquire(1)
}

func (l *wsLimiter) releaseInFlight() {
	if l == nil || l.inFlight == nil {
		return
	}

	l.inFlight.Release(1)
}

// wsConnSlots is the per-connection in-flight token bucket. A nil slots channel
// means the connection may run as many handlers as the global ceiling allows.
type wsConnSlots struct {
	slots chan struct{}
}

func newWSConnSlots(limit uint64) *wsConnSlots {
	if limit == 0 {
		return &wsConnSlots{}
	}

	return &wsConnSlots{slots: make(chan struct{}, limit)}
}

func (s *wsConnSlots) try() bool {
	if s == nil || s.slots == nil {
		return true
	}

	select {
	case s.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *wsConnSlots) release() {
	if s == nil || s.slots == nil {
		return
	}

	<-s.slots
}
