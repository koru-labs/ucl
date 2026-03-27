package ibft

import (
	"math/big"
	"testing"
	"time"

	"github.com/0xPolygon/polygon-edge/types"
	"github.com/0xPolygon/polygon-edge/validators"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// TestIBFTBackend_CalculateHeaderTimestamp verifies that the header timestamp
// is successfully calculated
func TestIBFTBackend_CalculateHeaderTimestamp(t *testing.T) {
	t.Parallel()

	// Reference time
	now := time.Unix(time.Now().UTC().Unix(), 0) // Round down

	testTable := []struct {
		name            string
		parentTimestamp int64
		currentTime     time.Time
		blockTime       uint64

		expectedTimestamp time.Time
	}{
		{
			"Valid clock block timestamp",
			now.Add(time.Duration(-1) * time.Second).Unix(), // 1s before
			now,
			1,
			now, // 1s after
		},
		{
			"Next multiple block clock",
			now.Add(time.Duration(-4) * time.Second).Unix(), // 4s before
			now,
			3,
			now, // now
		},
	}

	for _, testCase := range testTable {
		testCase := testCase

		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			i := &backendIBFT{
				blockTime: time.Duration(testCase.blockTime) * time.Second,
			}

			assert.Equal(
				t,
				testCase.expectedTimestamp.Unix(),
				i.calcHeaderTimestamp(
					uint64(testCase.parentTimestamp),
					testCase.currentTime,
				).Unix(),
			)
		})
	}
}

func TestIBFTBackend_GetVotingPowers(t *testing.T) {
	t.Parallel()

	validators := validators.NewECDSAValidatorSet(
		validators.NewECDSAValidator(types.StringToAddress("1")),
		validators.NewECDSAValidator(types.StringToAddress("2")),
	)

	forkManagerMock := &forkManagerMock{}
	forkManagerMock.On("GetValidators", mock.Anything).Return(validators)

	i := &backendIBFT{
		forkManager: forkManagerMock,
	}

	result, err := i.GetVotingPowers(1)
	assert.NoError(t, err)
	assert.Equal(t, big.NewInt(1), result[types.AddressToString(validators.At(0).Addr())])
	assert.Equal(t, big.NewInt(1), result[types.AddressToString(validators.At(1).Addr())])
}
