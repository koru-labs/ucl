package export

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/hashicorp/go-hclog"
	"golang.org/x/sync/singleflight"
)

// ServiceConfig bounds what one request may cost the node.
type ServiceConfig struct {
	// Workers is how many blocks execute at once, across all requests.
	Workers int
	// MaxBlocks caps the blocks of one range.
	MaxBlocks uint64
	// MaxBytes caps the frames of one range, except that a range always returns its first record.
	MaxBytes int
}

// DefaultServiceConfig leaves most of a full node's CPUs to importing ucl's new blocks.
var DefaultServiceConfig = ServiceConfig{Workers: 4, MaxBlocks: 1_000, MaxBytes: 64 << 20}

// Service is the export endpoint's engine: records on demand, cached on disk, each block executed
// by one job however many requests want it, on a fixed pool of workers.
type Service struct {
	exporter *Exporter
	chain    Chain
	cache    *Cache
	config   ServiceConfig
	logger   hclog.Logger

	group   singleflight.Group
	workers chan struct{}
}

func NewService(exporter *Exporter, chain Chain, cache *Cache, config ServiceConfig, logger hclog.Logger) *Service {
	if config.Workers < 1 {
		config.Workers = 1
	}

	if config.MaxBlocks < 1 {
		config.MaxBlocks = 1
	}

	return &Service{
		exporter: exporter,
		chain:    chain,
		cache:    cache,
		config:   config,
		logger:   logger,
		workers:  make(chan struct{}, config.Workers),
	}
}

// Frame returns block `number`'s frame, from the cache or by executing the block once.
func (s *Service) Frame(number uint64) ([]byte, error) {
	block, ok := s.chain.GetBlockByNumber(number, false)
	if !ok {
		return nil, fmt.Errorf("%w: %d", ErrUnknownBlock, number)
	}

	hash := block.Hash()
	if frame, ok := s.cache.Get(number, hash); ok {
		return frame, nil
	}

	frame, err, _ := s.group.Do(strconv.FormatUint(number, 10), func() (interface{}, error) {
		// Another job may have finished between the lookup above and this one starting.
		if frame, ok := s.cache.Get(number, hash); ok {
			return frame, nil
		}

		s.workers <- struct{}{}

		defer func() { <-s.workers }()

		record, err := s.exporter.Export(number)
		if err != nil {
			return nil, err
		}

		frame := EncodeFrame(record.MarshalRLP())
		if err := s.cache.Put(number, hash, frame); err != nil {
			// The record is still good; the next request computes it again.
			s.logger.Warn("export cache write failed", "block", number, "err", err)
		}

		return frame, nil
	})
	if err != nil {
		return nil, err
	}

	return frame.([]byte), nil //nolint:forcetypeassert
}

// RangeResult is what a range request returns.
type RangeResult struct {
	// Frames are the records of blocks From, From+1, … in order.
	Frames [][]byte
	// Next is the first block not returned: where the follower asks next.
	Next uint64
	// Head is the chain's head when the request was served.
	Head uint64
	// Err names the block that stopped the range, when one did; the frames before it are returned.
	Err *BlockError
}

// BlockError is a block that could not be exported.
type BlockError struct {
	Number  uint64
	Message string
	// Irreproducible is set when the block's re-execution does not match what ucl stored.
	Irreproducible bool
}

// Range returns the records of blocks from..to, capped at the chain's head, MaxBlocks and MaxBytes.
// Its blocks execute in parallel on the worker pool; the first one that fails ends the range.
func (s *Service) Range(from, to uint64) (*RangeResult, error) {
	if from == 0 {
		return nil, errors.New("ranges start at block 1: block 0 comes from the converted genesis")
	}

	if to < from {
		return nil, fmt.Errorf("empty range %d..%d", from, to)
	}

	head := s.chain.Header().Number
	result := &RangeResult{Next: from, Head: head}

	end := min(to, head, from+s.config.MaxBlocks-1)
	if from > end {
		return result, nil
	}

	type outcome struct {
		frame []byte
		err   error
	}

	outcomes := make([]chan outcome, end-from+1)
	for i := range outcomes {
		outcomes[i] = make(chan outcome, 1)

		go func(number uint64, out chan<- outcome) {
			frame, err := s.Frame(number)
			out <- outcome{frame, err}
		}(from+uint64(i), outcomes[i])
	}

	size := 0

	for i, ch := range outcomes {
		number := from + uint64(i)
		got := <-ch

		if got.err != nil {
			result.Err = &BlockError{
				Number:         number,
				Message:        got.err.Error(),
				Irreproducible: errors.Is(got.err, ErrIrreproducible),
			}

			break
		}

		if len(result.Frames) > 0 && size+len(got.frame) > s.config.MaxBytes {
			break
		}

		size += len(got.frame)
		result.Frames = append(result.Frames, got.frame)
		result.Next = number + 1
	}

	return result, nil
}
