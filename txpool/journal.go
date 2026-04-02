package txpool

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"sync"

	"github.com/0xPolygon/polygon-edge/types"
	"github.com/hashicorp/go-hclog"
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

	journalCh chan struct{} // used for sending journal rotation events to txpool

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
	// open the journal for loading any past transactions
	data, err := os.ReadFile(j.path)
	if errors.Is(err, fs.ErrNotExist) {
		// skip the parsing if the journal file doesn't exist at all
		return nil
	}

	txs := make([]*types.Transaction, 0)
	// decode txs
	for len(data) > 0 {
		tx := &types.Transaction{}
		if err := tx.UnmarshalJournal(data); err != nil {
			j.logger.Error("failed to decode journaled tx", "err", err)

			return err
		}

		data = data[tx.JournalSize():]
		txs = append(txs, tx)
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
func (j *journal) insert(tx *types.Transaction) error {
	j.lock.Lock()
	defer j.lock.Unlock()

	if j.writer == nil {
		return errors.New("no active journal")
	}

	_, err := j.writer.Write(tx.MarshalJournal())
	if err == nil {
		j.count++
		if j.count == j.rotateSize {
			// send event
			j.journalCh <- struct{}{}
		}
	}

	return err
}

// rotate regenerates the transaction journal based on the current contents of
// the transaction pool.
func (j *journal) rotate(local []*types.Transaction) error {
	j.lock.Lock()
	defer j.lock.Unlock()

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

	j.logger.Info("regenerated local transaction journal", "transactions", len(local))

	return nil
}

// close flushes the transaction journal contents to disk and closes the file.
func (j *journal) close() error {
	j.lock.Lock()
	defer j.lock.Unlock()

	var err error

	if j.writer != nil {
		err = j.writer.Close()
		j.writer = nil
	}

	return err
}
