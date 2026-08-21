package store

import (
	"github.com/0xPolygon/polygon-edge/consensus/ibft/signer"
	"github.com/0xPolygon/polygon-edge/types"
)

// Utilities for test
const (
	TestEpochSize = 100
)

func NewMockGetSigner(s signer.Signer) func(uint64) (signer.Signer, error) {
	return func(u uint64) (signer.Signer, error) {
		return s, nil
	}
}

type MockBlockchain struct {
	HeaderFn            func() *types.Header
	GetHeaderByNumberFn func(uint64) (*types.Header, bool)
}

func (m *MockBlockchain) Header() *types.Header {
	if m.HeaderFn == nil {
		return nil
	}

	return m.HeaderFn()
}

func (m *MockBlockchain) GetHeaderByNumber(height uint64) (*types.Header, bool) {
	if m.GetHeaderByNumberFn == nil {
		return nil, false
	}

	return m.GetHeaderByNumberFn(height)
}
