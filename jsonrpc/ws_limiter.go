package jsonrpc

import (
	"sync"

	"golang.org/x/sync/semaphore"
)

// wsLimiter caps how many websocket connections the node accepts and how many
// requests may be in flight at once. A nil limiter, or a zero ceiling, means
// that particular limit is disabled.
type wsLimiter struct {
	maxConns uint64

	// connsMu guards conns and the gauge published from it. A mutex rather than
	// an atomic because the published gauge is last-write-wins: counting and
	// publishing separately lets two connections interleave their writes and
	// leave the gauge holding a count that no longer matches reality.
	connsMu sync.Mutex
	conns   int64

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

	l.connsMu.Lock()
	defer l.connsMu.Unlock()

	if l.maxConns != 0 && uint64(l.conns) >= l.maxConns {
		incrRejected(rejectWSConnLimit)

		return false
	}

	l.conns++
	setWSConnections(l.conns)

	return true
}

func (l *wsLimiter) removeConn() {
	if l == nil {
		return
	}

	l.connsMu.Lock()
	defer l.connsMu.Unlock()

	l.conns--
	setWSConnections(l.conns)
}

func (l *wsLimiter) connCount() int64 {
	if l == nil {
		return 0
	}

	l.connsMu.Lock()
	defer l.connsMu.Unlock()

	return l.conns
}

func (l *wsLimiter) tryAcquireInFlight() bool {
	if l == nil || l.inFlight == nil {
		return true
	}

	if !l.inFlight.TryAcquire(1) {
		incrRejected(rejectWSInFlight)

		return false
	}

	return true
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
		incrRejected(rejectWSInFlightConn)

		return false
	}
}

func (s *wsConnSlots) release() {
	if s == nil || s.slots == nil {
		return
	}

	<-s.slots
}
