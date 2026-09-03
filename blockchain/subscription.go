package blockchain

import (
	"math/big"
	"sync"

	"github.com/0xPolygon/polygon-edge/types"
	"github.com/hashicorp/go-metrics"
)

type void struct{}

const (
	// blockchainMetrics is the metrics prefix for the blockchain package
	blockchainMetrics = "blockchain"

	// internalSubscriptionQueueSize is the queue depth for node-internal subscribers
	// (consensus, syncer, sync progression). It is a memory backstop rather than an expected
	// limit: a queued event owns a deep copy of its headers, roughly 600B plus ExtraData,
	// and an IBFT ExtraData carries the validator set plus a 65 byte committed seal per
	// validator, so a large set puts an event in the low KB range. 1024 therefore bounds a
	// wedged subscriber at single digit MB, while still buffering over half an hour of
	// events at a 2s block time.
	internalSubscriptionQueueSize = 1024

	// externalSubscriptionQueueSize is the queue depth for subscribers that relay events to
	// remote clients (JSON-RPC filters, gRPC System.Subscribe). Those are expected to lag,
	// and there can be one per client, so their queue is kept small.
	externalSubscriptionQueueSize = 128
)

var (
	// internalDroppedEvents counts events lost by node-internal subscribers. A non-zero value
	// means an internal consumer stopped draining its queue, which is not expected.
	internalDroppedEvents = []string{blockchainMetrics, "internal_subscription_events_dropped"}

	// externalDroppedEvents counts events lost by subscribers that relay to remote clients,
	// which happens whenever a client cannot keep up with the chain.
	externalDroppedEvents = []string{blockchainMetrics, "subscription_events_dropped"}
)

// Subscription is the blockchain subscription interface
type Subscription interface {
	GetEventCh() chan *Event
	GetEvent() *Event
}

// FOR TESTING PURPOSES //

type MockSubscription struct {
	*subscription
}

func NewMockSubscription() *MockSubscription {
	return &MockSubscription{
		subscription: &subscription{
			updateCh: make(chan *Event),
			closeCh:  make(chan void),
		},
	}
}
func (m *MockSubscription) Push(e *Event) {
	m.updateCh <- e
}

// subscription is the Blockchain event subscription object
type subscription struct {
	updateCh chan *Event // Channel for update information
	closeCh  chan void   // Channel for close signals

	// pushMux serializes making room in a full queue, so that concurrent pushes cannot
	// reorder events
	pushMux sync.Mutex

	// dropMetric is the counter incremented whenever this subscription loses an event
	dropMetric []string
}

// enqueue adds the event to the subscription queue and reports whether it was delivered
// without losing an event.
//
// It must never block. It runs on the block write path with the blockchain write lock held,
// so a blocking send here stops the node from committing blocks entirely. When the queue is
// full the oldest event is discarded instead, which keeps a lagging subscriber converging on
// the current head rather than being pinned to a stale event.
func (s *subscription) enqueue(event *Event) bool {
	s.pushMux.Lock()
	defer s.pushMux.Unlock()

	select {
	case s.updateCh <- event:
		return true
	default:
	}

	// The queue is full, discard the oldest event to make room for the newest one
	select {
	case <-s.updateCh:
	default:
	}

	select {
	case s.updateCh <- event:
	default:
	}

	return false
}

// GetEventCh creates a new event channel, and returns it
func (s *subscription) GetEventCh() chan *Event {
	return s.updateCh
}

// GetEvent returns the event from the subscription (BLOCKING)
func (s *subscription) GetEvent() *Event {
	// Wait for an update
	select {
	case ev := <-s.updateCh:
		return ev
	case <-s.closeCh:
		return nil
	}
}

type EventType int

const (
	EventHead  EventType = iota // New head event
	EventReorg                  // Chain reorganization event
	EventFork                   // Chain fork event
)

// Event is the blockchain event that gets passed to the listeners
type Event struct {
	// Old chain (removed headers) if there was a reorg
	OldChain []*types.Header

	// New part of the chain (or a fork)
	NewChain []*types.Header

	// Difficulty is the new difficulty created with this event
	Difficulty *big.Int

	// Type is the type of event
	Type EventType

	// Source is the source that generated the blocks for the event
	// right now it can be either the Sealer or the Syncer
	Source string
}

// Header returns the latest block header for the event
func (e *Event) Header() *types.Header {
	return e.NewChain[len(e.NewChain)-1]
}

// SetDifficulty sets the event difficulty
func (e *Event) SetDifficulty(b *big.Int) {
	e.Difficulty = new(big.Int).Set(b)
}

// AddNewHeader appends a header to the event's NewChain array
func (e *Event) AddNewHeader(newHeader *types.Header) {
	header := newHeader.Copy()

	if e.NewChain == nil {
		// Array doesn't exist yet, create it
		e.NewChain = []*types.Header{}
	}

	e.NewChain = append(e.NewChain, header)
}

// AddOldHeader appends a header to the event's OldChain array
func (e *Event) AddOldHeader(oldHeader *types.Header) {
	header := oldHeader.Copy()

	if e.OldChain == nil {
		// Array doesn't exist yet, create it
		e.OldChain = []*types.Header{}
	}

	e.OldChain = append(e.OldChain, header)
}

// SubscribeEvents returns a blockchain event subscription for node-internal consumers,
// which are not expected to fall behind
func (b *Blockchain) SubscribeEvents() Subscription {
	return b.stream.subscribe(internalSubscriptionQueueSize, internalDroppedEvents)
}

// SubscribeEventsLossy returns a blockchain event subscription for consumers that relay
// events to remote clients. Its queue is deliberately small, since a client that stops
// reading must not be able to make the node retain events on its behalf.
func (b *Blockchain) SubscribeEventsLossy() Subscription {
	return b.stream.subscribe(externalSubscriptionQueueSize, externalDroppedEvents)
}

// UnsubscribeEvents removes subscription from blockchain event stream
func (b *Blockchain) UnsubscribeEvents(sub Subscription) {
	if subPtr, ok := sub.(*subscription); ok {
		b.stream.unsubscribe(subPtr)
	} else {
		b.logger.Warn("Failed to unsubscribe from event stream. Invalid subscription.")
	}
}

// eventStream is the structure that contains the subscribers
// which it uses to notify of updates
type eventStream struct {
	sync.RWMutex
	subscriptions map[*subscription]struct{}
}

// newEventStream creates event stream and initializes subscriptions map
func newEventStream() *eventStream {
	return &eventStream{
		subscriptions: make(map[*subscription]struct{}),
	}
}

// subscribe creates a new blockchain event subscription
func (e *eventStream) subscribe(queueSize int, dropMetric []string) *subscription {
	sub := &subscription{
		updateCh:   make(chan *Event, queueSize),
		closeCh:    make(chan void),
		dropMetric: dropMetric,
	}

	e.Lock()
	e.subscriptions[sub] = struct{}{}
	e.Unlock()

	return sub
}

func (e *eventStream) unsubscribe(sub *subscription) {
	e.Lock()
	defer e.Unlock()

	delete(e.subscriptions, sub)
	close(sub.closeCh)
}

// push adds a new Event, and notifies listeners.
//
// This runs on the block write path with the blockchain write lock held, so it must never
// block on a subscriber. It also must not hold the read lock for long, since unsubscribe
// needs the write lock to make progress.
func (e *eventStream) push(event *Event) {
	e.RLock()
	defer e.RUnlock()

	// Notify the listeners
	for sub := range e.subscriptions {
		if !sub.enqueue(event) {
			metrics.IncrCounter(sub.dropMetric, 1)
		}
	}
}
