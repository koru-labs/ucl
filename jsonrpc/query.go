package jsonrpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/0xPolygon/polygon-edge/types"
)

var (
	ErrExceedMaxAddresses = errors.New("exceed max addresses")
	ErrExceedMaxTopics    = errors.New("exceed max topics")
)

const (
	// DefaultMaxLogQueryAddresses is the cap on addresses in one log query.
	DefaultMaxLogQueryAddresses uint64 = 1000

	// DefaultMaxLogQueryTopicPositions is the geth-style cap on topic positions
	// (Ethereum logs have at most 4: event signature + up to 3 indexed fields).
	DefaultMaxLogQueryTopicPositions uint64 = 4

	// DefaultMaxLogQueryResults is the maximum number of logs one query may return.
	DefaultMaxLogQueryResults uint64 = 10_000

	// DefaultMaxLogQueryReceipts is the maximum number of receipts a range query
	// may examine. This is the blocks×receipts work budget, distinct from the
	// block-span cap.
	DefaultMaxLogQueryReceipts uint64 = 10_000
)

// LogQueryLimits caps log-filter complexity. A zero field means the default.
type LogQueryLimits struct {
	MaxAddresses      uint64
	MaxTopicPositions uint64
	MaxLogs           uint64
	MaxReceipts       uint64
}

// DefaultLogQueryLimits returns the geth-style address and topic-position caps.
func DefaultLogQueryLimits() LogQueryLimits {
	return LogQueryLimits{
		MaxAddresses:      DefaultMaxLogQueryAddresses,
		MaxTopicPositions: DefaultMaxLogQueryTopicPositions,
		MaxLogs:           DefaultMaxLogQueryResults,
		MaxReceipts:       DefaultMaxLogQueryReceipts,
	}
}

func (l LogQueryLimits) withDefaults() LogQueryLimits {
	if l.MaxAddresses == 0 {
		l.MaxAddresses = DefaultMaxLogQueryAddresses
	}

	if l.MaxTopicPositions == 0 {
		l.MaxTopicPositions = DefaultMaxLogQueryTopicPositions
	}

	if l.MaxLogs == 0 {
		l.MaxLogs = DefaultMaxLogQueryResults
	}

	if l.MaxReceipts == 0 {
		l.MaxReceipts = DefaultMaxLogQueryReceipts
	}

	return l
}

var activeLogQueryLimits atomic.Pointer[LogQueryLimits]

func setActiveLogQueryLimits(l LogQueryLimits) {
	l = l.withDefaults()
	activeLogQueryLimits.Store(&l)
}

func getActiveLogQueryLimits() LogQueryLimits {
	if p := activeLogQueryLimits.Load(); p != nil {
		return *p
	}

	return DefaultLogQueryLimits()
}

// LogQuery is a query to filter logs
type LogQuery struct {
	BlockHash *types.Hash

	fromBlock BlockNumber
	toBlock   BlockNumber

	Addresses []types.Address
	Topics    [][]types.Hash

	addressSet map[types.Address]struct{}
}

// addTopicSet adds specific topics to the log filter topics
func (q *LogQuery) addTopicSet(limits LogQueryLimits, set ...string) error {
	if limits.MaxTopicPositions != 0 && uint64(len(q.Topics)+1) > limits.MaxTopicPositions {
		return ErrExceedMaxTopics
	}

	// Each topic position is an OR-set; reuse the address list cap so neither
	// list can be inflated independently.
	if limits.MaxAddresses != 0 && uint64(len(set)) > limits.MaxAddresses {
		return ErrExceedMaxTopics
	}

	if q.Topics == nil {
		q.Topics = [][]types.Hash{}
	}

	res := []types.Hash{}

	for _, i := range set {
		item := types.Hash{}
		if err := item.UnmarshalText([]byte(i)); err != nil {
			return err
		}

		res = append(res, item)
	}

	q.Topics = append(q.Topics, res)

	return nil
}

// addAddress Adds the address to the log filter
func (q *LogQuery) addAddress(limits LogQueryLimits, raw string) error {
	if limits.MaxAddresses != 0 && uint64(len(q.Addresses)+1) > limits.MaxAddresses {
		return ErrExceedMaxAddresses
	}

	if q.Addresses == nil {
		q.Addresses = []types.Address{}
	}

	addr := types.Address{}

	if err := addr.UnmarshalText([]byte(raw)); err != nil {
		return err
	}

	q.Addresses = append(q.Addresses, addr)
	q.indexAddress(addr)

	return nil
}

func (q *LogQuery) indexAddress(addr types.Address) {
	if q.addressSet == nil {
		q.addressSet = make(map[types.Address]struct{})
	}

	q.addressSet[addr] = struct{}{}
}

func (q *LogQuery) hasAddressOrTopic() bool {
	if len(q.Addresses) > 0 {
		return true
	}

	for _, sub := range q.Topics {
		if len(sub) > 0 {
			return true
		}
	}

	return false
}

func (q *LogQuery) indexAddresses() {
	if q.addressSet != nil || len(q.Addresses) == 0 {
		return
	}

	q.addressSet = make(map[types.Address]struct{}, len(q.Addresses))
	for _, addr := range q.Addresses {
		q.addressSet[addr] = struct{}{}
	}
}

func decodeLogQueryFromInterface(i interface{}) (*LogQuery, error) {
	// once the log filter is decoded as map[string]interface we cannot use unmarshal json
	raw, err := json.Marshal(i)
	if err != nil {
		return nil, err
	}

	query := &LogQuery{}
	if err := unmarshalLogQuery(query, raw, getActiveLogQueryLimits()); err != nil {
		return nil, err
	}

	return query, nil
}

// UnmarshalJSON decodes a json object
func (q *LogQuery) UnmarshalJSON(data []byte) error {
	return unmarshalLogQuery(q, data, getActiveLogQueryLimits())
}

func unmarshalLogQuery(q *LogQuery, data []byte, limits LogQueryLimits) error {
	limits = limits.withDefaults()

	var obj struct {
		BlockHash *types.Hash   `json:"blockHash"`
		FromBlock string        `json:"fromBlock"`
		ToBlock   string        `json:"toBlock"`
		Address   interface{}   `json:"address"`
		Topics    []interface{} `json:"topics"`
	}

	err := json.Unmarshal(data, &obj)
	if err != nil {
		return err
	}

	q.BlockHash = obj.BlockHash

	// pending from/to blocks or "" is treated as a latest block
	if q.fromBlock, err = stringToBlockNumberSafe(obj.FromBlock); err != nil {
		return err
	}

	if q.toBlock, err = stringToBlockNumberSafe(obj.ToBlock); err != nil {
		return err
	}

	if err = q.decodeAddresses(obj.Address, limits); err != nil {
		return err
	}

	return q.decodeTopics(obj.Topics, limits)
}

func (q *LogQuery) decodeAddresses(raw interface{}, limits LogQueryLimits) error {
	if raw == nil {
		return nil
	}

	switch addr := raw.(type) {
	case string:
		return q.addAddress(limits, addr)

	case []interface{}:
		if limits.MaxAddresses != 0 && uint64(len(addr)) > limits.MaxAddresses {
			return ErrExceedMaxAddresses
		}

		for _, item := range addr {
			value, ok := item.(string)
			if !ok {
				return fmt.Errorf("address expected")
			}

			if err := q.addAddress(limits, value); err != nil {
				return err
			}
		}

		return nil

	default:
		return fmt.Errorf("failed to decode address. Expected either '' or ['', '']")
	}
}

func (q *LogQuery) decodeTopics(topics []interface{}, limits LogQueryLimits) error {
	if topics == nil {
		return nil
	}

	if limits.MaxTopicPositions != 0 && uint64(len(topics)) > limits.MaxTopicPositions {
		return ErrExceedMaxTopics
	}

	for _, item := range topics {
		switch raw := item.(type) {
		case string:
			if err := q.addTopicSet(limits, raw); err != nil {
				return err
			}

		case []interface{}:
			if limits.MaxAddresses != 0 && uint64(len(raw)) > limits.MaxAddresses {
				return ErrExceedMaxTopics
			}

			res := make([]string, 0, len(raw))

			for _, i := range raw {
				value, ok := i.(string)
				if !ok {
					return fmt.Errorf("hash expected")
				}

				res = append(res, value)
			}

			if err := q.addTopicSet(limits, res...); err != nil {
				return err
			}

		case nil:
			if err := q.addTopicSet(limits); err != nil {
				return err
			}

		default:
			return fmt.Errorf("failed to decode topics. Expected '' or [''] or null")
		}
	}

	return nil
}

// MatchBloom reports whether the block bloom could contain a log that matches this query.
// A zero bloom is treated as unknown (IBFT headers do not populate LogsBloom), so the
// caller must fall through to per-log matching.
func (q *LogQuery) MatchBloom(bloom types.Bloom) bool {
	if bloom == (types.Bloom{}) {
		return true
	}

	if len(q.Addresses) > 0 {
		found := false

		for _, addr := range q.Addresses {
			if bloom.Contains(addr.Bytes()) {
				found = true

				break
			}
		}

		if !found {
			return false
		}
	}

	for _, sub := range q.Topics {
		if len(sub) == 0 {
			continue
		}

		found := false

		for _, topic := range sub {
			if bloom.Contains(topic.Bytes()) {
				found = true

				break
			}
		}

		if !found {
			return false
		}
	}

	return true
}

// Match returns whether the receipt includes topics for this filter
func (q *LogQuery) Match(log *types.Log) bool {
	if len(q.Addresses) > 0 {
		q.indexAddresses()

		if _, ok := q.addressSet[log.Address]; !ok {
			return false
		}
	}

	if len(q.Topics) > len(log.Topics) {
		return false
	}

	for i, sub := range q.Topics {
		match := len(sub) == 0

		for _, topic := range sub {
			if log.Topics[i] == topic {
				match = true

				break
			}
		}

		if !match {
			return false
		}
	}

	return true
}
