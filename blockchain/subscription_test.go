package blockchain

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/0xPolygon/polygon-edge/blockchain/storage/memory"
	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/hashicorp/go-hclog"
	lru "github.com/hashicorp/golang-lru"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// subscribeForTest creates a subscription on the internal tier
func subscribeForTest(e *eventStream) *subscription {
	return e.subscribe(internalSubscriptionQueueSize, internalDroppedEvents)
}

func TestSubscription(t *testing.T) {
	t.Parallel()

	var (
		e              = newEventStream()
		sub            = subscribeForTest(e)
		caughtEventNum = uint64(0)
		event          = &Event{
			NewChain: []*types.Header{
				{
					Number: 100,
				},
			},
		}

		wg sync.WaitGroup
	)

	defer e.unsubscribe(sub)

	updateCh := sub.GetEventCh()

	wg.Add(1)

	go func() {
		defer wg.Done()

		select {
		case ev := <-updateCh:
			caughtEventNum = ev.NewChain[0].Number
		case <-time.After(5 * time.Second):
		}
	}()

	// Send the event to the channel
	e.push(event)

	// Wait for the event to be parsed
	wg.Wait()

	assert.Equal(t, event.NewChain[0].Number, caughtEventNum)
}

// TestSubscription_ConcurrentPushAndConsume stresses the non-blocking fan-out. Delivery is no
// longer guaranteed, since a producer that outruns a subscriber discards that subscriber's
// oldest events rather than waiting for it. What is guaranteed is that whatever a subscriber
// does receive stays in order, and that it always ends up on the most recent event.
func TestSubscription_ConcurrentPushAndConsume(t *testing.T) {
	t.Parallel()

	const (
		numOfEvents        = 100000
		numOfSubscriptions = 10
		lastEvent          = uint64(numOfEvents - 1)
	)

	var (
		e  = newEventStream()
		wg sync.WaitGroup
	)

	subscriptions := make([]*subscription, numOfSubscriptions)
	errCh := make(chan error, numOfSubscriptions)

	worker := func(id int, sub *subscription) {
		defer wg.Done()

		updateCh := sub.GetEventCh()
		previous := uint64(0)

		for {
			select {
			case evnt := <-updateCh:
				number := evnt.NewChain[0].Number

				if number < previous {
					errCh <- fmt.Errorf("subscription %d got event %d after %d", id, number, previous)

					return
				}

				previous = number

				if number == lastEvent {
					return
				}
			case <-time.After(10 * time.Second):
				errCh <- fmt.Errorf(
					"subscription %d never received the most recent event, stopped at %d", id, previous,
				)

				return
			}
		}
	}

	wg.Add(numOfSubscriptions)

	for i := 0; i < numOfSubscriptions; i++ {
		subscriptions[i] = subscribeForTest(e)

		go worker(i, subscriptions[i])
	}

	// Send the events to the channels
	for i := 0; i < numOfEvents; i++ {
		e.push(&Event{NewChain: []*types.Header{{Number: uint64(i)}}})
	}

	// Wait for the events to be processed
	wg.Wait()
	close(errCh)

	for err := range errCh {
		assert.NoError(t, err)
	}

	for _, s := range subscriptions {
		e.unsubscribe(s)
	}
}

// TestSubscription_EnqueueDropsOldestWhenFull pins the overflow policy: the newest event always
// wins, so a subscriber that falls behind converges on the current head instead of being pinned
// to stale events. Every internal consumer of this stream only acts on the most recent event.
func TestSubscription_EnqueueDropsOldestWhenFull(t *testing.T) {
	t.Parallel()

	e := newEventStream()
	sub := e.subscribe(2, externalDroppedEvents)

	defer e.unsubscribe(sub)

	newEvent := func(number uint64) *Event {
		return &Event{NewChain: []*types.Header{{Number: number}}}
	}

	require.True(t, sub.enqueue(newEvent(1)))
	require.True(t, sub.enqueue(newEvent(2)))

	// The queue is full, so these report that an event was lost
	require.False(t, sub.enqueue(newEvent(3)))
	require.False(t, sub.enqueue(newEvent(4)))

	// The two oldest events were the ones discarded
	assert.Equal(t, uint64(3), sub.GetEvent().NewChain[0].Number)
	assert.Equal(t, uint64(4), sub.GetEvent().NewChain[0].Number)
}

// TestSubscription_PushNeverBlocksOnStalledSubscriber covers the core of the reported DoS: a
// subscriber that never reads its channel used to fill its queue and then block push, which runs
// with the blockchain write lock held.
func TestSubscription_PushNeverBlocksOnStalledSubscriber(t *testing.T) {
	t.Parallel()

	e := newEventStream()

	// Neither subscriber ever reads, on either tier
	internal := subscribeForTest(e)
	lossy := e.subscribe(externalSubscriptionQueueSize, externalDroppedEvents)

	defer func() {
		e.unsubscribe(internal)
		e.unsubscribe(lossy)
	}()

	const numOfEvents = internalSubscriptionQueueSize * 2

	pushed := make(chan struct{})

	go func() {
		defer close(pushed)

		for i := 0; i < numOfEvents; i++ {
			e.push(&Event{NewChain: []*types.Header{{Number: uint64(i)}}})
		}
	}()

	select {
	case <-pushed:
	case <-time.After(10 * time.Second):
		t.Fatal("push blocked on a subscriber that never reads its events")
	}

	// The queue retains the most recent window of events, so a stalled subscriber that
	// resumes still catches up to the current head
	events := make([]*Event, 0, internalSubscriptionQueueSize)

	for i := 0; i < internalSubscriptionQueueSize; i++ {
		events = append(events, internal.GetEvent())
	}

	assert.Equal(t, uint64(internalSubscriptionQueueSize), events[0].NewChain[0].Number)
	assert.Equal(t, uint64(numOfEvents-1), events[len(events)-1].NewChain[0].Number)
}

// TestSubscription_UnsubscribeWhileSubscriberStalled covers why the node used to be unable to
// recover: push held the stream read lock while blocked, and unsubscribe needs the write lock.
func TestSubscription_UnsubscribeWhileSubscriberStalled(t *testing.T) {
	t.Parallel()

	e := newEventStream()
	stalled := subscribeForTest(e)

	for i := 0; i < internalSubscriptionQueueSize+10; i++ {
		e.push(&Event{})
	}

	unsubscribed := make(chan struct{})

	go func() {
		defer close(unsubscribed)

		e.unsubscribe(stalled)
	}()

	select {
	case <-unsubscribed:
	case <-time.After(5 * time.Second):
		t.Fatal("unsubscribe blocked while a subscriber was not reading its events")
	}
}

// TestBlockchain_SubscriptionTiers asserts that a subscription driven by remote clients cannot
// make the node retain as many events as a node-internal one
func TestBlockchain_SubscriptionTiers(t *testing.T) {
	t.Parallel()

	b := &Blockchain{stream: newEventStream()}

	internal := b.SubscribeEvents()
	lossy := b.SubscribeEventsLossy()

	defer func() {
		b.UnsubscribeEvents(internal)
		b.UnsubscribeEvents(lossy)
	}()

	assert.Equal(t, internalSubscriptionQueueSize, cap(internal.GetEventCh()))
	assert.Equal(t, externalSubscriptionQueueSize, cap(lossy.GetEventCh()))
	assert.Less(t, cap(lossy.GetEventCh()), cap(internal.GetEventCh()))
}

// newSubscriptionTestBlockchain builds the minimal blockchain needed to drive the block write
// path, which is where events are dispatched from
func newSubscriptionTestBlockchain(t *testing.T) *Blockchain {
	t.Helper()

	storageMock, err := memory.NewMemoryStorage()
	require.NoError(t, err)

	bc := &Blockchain{
		logger:    hclog.NewNullLogger(),
		db:        storageMock,
		consensus: &MockVerifier{},
		txSigner:  &mockSigner{},
		stream:    newEventStream(),
		gpAverage: &gasPriceAverage{count: new(big.Int)},
		config: &chain.Chain{
			Params:  &chain.Params{Forks: &chain.Forks{}},
			Genesis: &chain.Genesis{},
		},
	}

	bc.headersCache, err = lru.New(10)
	require.NoError(t, err)

	bc.difficultyCache, err = lru.New(10)
	require.NoError(t, err)

	genesis := &types.Header{Number: 0}
	genesis.ComputeHash()

	bc.currentHeader.Store(genesis)
	bc.currentDifficulty.Store(big.NewInt(1))
	bc.difficultyCache.Add(genesis.Hash, big.NewInt(1))

	return bc
}

// TestBlockchain_WriteBlockNotDelayedByStalledSubscriber is the end-to-end regression test for
// the reported DoS. dispatchEvent runs while the blockchain write lock is held, so a fan-out
// that blocked on one subscriber stopped the node from committing blocks at all.
func TestBlockchain_WriteBlockNotDelayedByStalledSubscriber(t *testing.T) {
	t.Parallel()

	bc := newSubscriptionTestBlockchain(t)

	// Stand in for a WS client and a gRPC client that both stopped reading
	internal := bc.SubscribeEvents()
	lossy := bc.SubscribeEventsLossy()

	defer func() {
		bc.UnsubscribeEvents(internal)
		bc.UnsubscribeEvents(lossy)
	}()

	// Well past both queue capacities
	const numOfBlocks = uint64(internalSubscriptionQueueSize + 10)

	var (
		done  = make(chan struct{})
		errCh = make(chan error, 1)
	)

	go func() {
		defer close(done)

		for number := uint64(1); number <= numOfBlocks; number++ {
			header := &types.Header{
				Number:     number,
				ParentHash: bc.Header().Hash,
			}
			header.ComputeHash()

			if err := bc.WriteFullBlock(&types.FullBlock{
				Block: &types.Block{Header: header},
			}, "test"); err != nil {
				errCh <- err

				return
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("block writes were delayed by subscribers that never read their events")
	}

	select {
	case err := <-errCh:
		require.NoError(t, err)
	default:
	}

	assert.Equal(t, numOfBlocks, bc.Header().Number)
}

func TestSubscription_AfterOneUnsubscribe(t *testing.T) {
	t.Parallel()

	var (
		e = newEventStream()

		wg sync.WaitGroup
	)

	sub1 := subscribeForTest(e)
	sub2 := subscribeForTest(e)

	wg.Add(2)

	worker := func(sub *subscription, expectedBlockCount uint8) {
		defer wg.Done()

		updateCh := sub.GetEventCh()
		receivedBlockCount := uint8(0)

		for {
			select {
			case <-updateCh:
				receivedBlockCount++
				if receivedBlockCount == expectedBlockCount {
					e.unsubscribe(sub)

					return
				}
			case <-time.After(10 * time.Second):
				e.unsubscribe(sub)
				t.Errorf("subscription did not caught all events")
			}
		}
	}

	go worker(sub1, 10)
	go worker(sub2, 20)

	// Send the events to the channels
	for i := 0; i < 20; i++ {
		e.push(&Event{})
		time.Sleep(time.Millisecond)
	}

	// Wait for the event to be parsed
	wg.Wait()
}

func TestSubscription_NilEventAfterClosingSubscription(t *testing.T) {
	t.Parallel()

	var (
		e = newEventStream()

		wg sync.WaitGroup
	)

	sub := subscribeForTest(e)

	wg.Add(1)

	receivedEvtCount := 0

	worker := func(sub *subscription) {
		defer wg.Done()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		for {
			select {
			case <-ctx.Done():
				t.Errorf("subscription did not caught all events")

				return
			default:
				evt := sub.GetEvent()
				if evt != nil {
					receivedEvtCount++
				} else {
					return
				}
			}
		}
	}

	go worker(sub)

	// Send the events to the channels
	for i := 0; i < 2; i++ {
		e.push(&Event{})
		time.Sleep(time.Millisecond)
	}

	// Wait for the events to be parsed before unsubscribe
	<-time.After(time.Second)
	e.unsubscribe(sub)

	// Wait for the worker to complete
	wg.Wait()

	assert.Equal(t, 2, receivedEvtCount)
}
