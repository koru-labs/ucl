package txpool

import (
	"errors"
	"io"
	"os"
	"sync"

	"github.com/0xPolygon/polygon-edge/types"
	"github.com/hashicorp/go-hclog"
)

var (
	errNoActiveJournal = errors.New("no active journal")
	errJournalClosed   = errors.New("journal closed")
)

// devNull is a WriteCloser that just discards anything written into it. Its
// goal is to allow the transaction journal to write into a fake journal when
// loading transactions on startup without printing warnings due to no file
// being read for write.
type devNull struct{}

func (*devNull) Write(p []byte) (n int, err error) { return len(p), nil }
func (*devNull) Close() error                      { return nil }

// journal is a rotating log of transactions with the aim of storing locally
// created transactions to allow non-executed ones to survive node restarts.
type journal struct {
	path   string         // filesystem path to store the transactions at
	writer io.WriteCloser // output stream to write new transactions into
	logger hclog.Logger   // logger
	count  uint64         // number of txs in journal
	lock   sync.Mutex     // used for journal locking during rotation
	closed bool           // set by close; no further writes or rotations

	// journalCh carries rotation requests to the txpool. It should have a
	// buffer of 1: requests are sent without blocking and a pending one
	// already covers any request that arrives before it is served.
	journalCh chan struct{}

	rotateSize uint64 // number of local txs in journal when rotate will be executed
}

// newTxJournal creates a new transaction journal to
func newTxJournal(path string, logger hclog.Logger, journalCh chan struct{}, rotateSize uint64) *journal {
	return &journal{
		path:       path,
		logger:     logger,
		journalCh:  journalCh,
		rotateSize: rotateSize,
	}
}

// load parses a transaction journal dump from disk, loading its contents into
// the specified pool.
func (j *journal) load(add func(*types.Transaction) error) error {
	txs, err := ReadJournalFile(j.path)
	if err != nil {
		j.logger.Error("failed to decode journaled tx", "err", err)

		return err
	}

	// temporarily discard any journal additions (don't double add on load)
	j.writer = new(devNull)

	defer func() { j.writer = nil }()

	dropped := 0
	// inject all transactions from the journal into the pool
	for _, tx := range txs {
		if err := add(tx); err != nil {
			dropped++

			j.logger.Debug("failed to add journaled transaction", "err", err)
		}
	}

	j.logger.Info("loaded local transaction journal", "transactions", len(txs), "dropped", dropped)

	return nil
}

// insert adds the specified transaction to the local disk journal.
// It never blocks on the rotation consumer. When the journal needs to be
// regenerated (size threshold reached, no open writer after a failed
// rotation, or a failed append) it requests a rotation after releasing the
// lock.
func (j *journal) insert(tx *types.Transaction) error {
	needRotate, err := j.write(tx)
	if needRotate {
		j.requestRotate()
	}

	return err
}

// write appends tx under the lock and reports whether a rotation is needed.
func (j *journal) write(tx *types.Transaction) (bool, error) {
	j.lock.Lock()
	defer j.lock.Unlock()

	if j.closed {
		return false, errJournalClosed
	}

	if j.writer == nil {
		// the last rotation failed; ask for another attempt so the journal
		// recovers once the cause (e.g. a full disk) is gone
		return true, errNoActiveJournal
	}

	if _, err := j.writer.Write(tx.MarshalJournal()); err != nil {
		// the tail may be partially written; regenerating from the pool
		// replaces it with a clean file
		return true, err
	}

	j.count++

	return j.count >= j.rotateSize, nil
}

// requestRotate signals the txpool to rotate the journal without blocking.
// If a request is already pending, this one is redundant and dropped.
func (j *journal) requestRotate() {
	select {
	case j.journalCh <- struct{}{}:
	default:
	}
}

// rotate regenerates the transaction journal based on the current contents of
// the transaction pool.
func (j *journal) rotate(local []*types.Transaction) error {
	j.lock.Lock()
	defer j.lock.Unlock()

	if j.closed {
		return nil
	}

	// close the current journal (if any is open)
	if j.writer != nil {
		if err := j.writer.Close(); err != nil {
			return err
		}

		j.writer = nil
	}

	// generate a new journal with the contents of the current pool
	replacement, err := os.OpenFile(j.path+".new", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644) //nolint:gosec
	if err != nil {
		return err
	}

	// fill a new journal with txs from the pool
	for _, tx := range local {
		_, err := replacement.Write(tx.MarshalJournal())
		if err != nil {
			_ = replacement.Close()

			return err
		}
	}

	if err = replacement.Close(); err != nil {
		return err
	}

	// replace the live journal with the newly generated one
	if err = os.Rename(j.path+".new", j.path); err != nil {
		return err
	}

	// open a new journal
	sink, err := os.OpenFile(j.path, os.O_WRONLY|os.O_APPEND, 0644) //nolint:gosec
	if err != nil {
		return err
	}

	// this reset is under mutex
	j.count = 0
	j.writer = sink

	// a request queued before this rotation is now stale
	select {
	case <-j.journalCh:
	default:
	}

	j.logger.Info("regenerated local transaction journal", "transactions", len(local))

	return nil
}

// close flushes the transaction journal contents to disk and closes the file.
func (j *journal) close() error {
	j.lock.Lock()
	defer j.lock.Unlock()

	var err error

	j.closed = true

	if j.writer != nil {
		err = j.writer.Close()
		j.writer = nil
	}

	return err
}
