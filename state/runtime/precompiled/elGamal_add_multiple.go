package precompiled

import (
	"fmt"
	"github.com/0xPolygon/polygon-edge/chain"
	crypto "github.com/0xPolygon/polygon-edge/helper/elgamal"
	"github.com/0xPolygon/polygon-edge/state/runtime"
	"github.com/0xPolygon/polygon-edge/types"
	"log"
	"math/big"
)

type elGamalAddMultiple struct{}

func (c *elGamalAddMultiple) gas(input []byte, _ *chain.ForksInTime) uint64 {
	// each cipher is 128 bytes, so 256 bytes is 2 ciphers
	baseGas := uint64(21000)
	numCiphers := len(input) / 128
	if numCiphers < 1 {
		return baseGas
	}
	// add 10000 gas for each additional cipher
	extraGas := uint64(10000) * uint64(numCiphers-1)
	return baseGas + extraGas
}

func (c *elGamalAddMultiple) run(input []byte, caller types.Address, host runtime.Host) ([]byte, error) {
	log.Printf("elGamalAddMultiple is called with input length: %d", len(input))

	// Check if input length is a multiple of 128 (each cipher contains two points, 64 bytes per point)
	if len(input)%128 != 0 {
		return nil, fmt.Errorf("input length must be a multiple of 128 bytes")
	}

	// At least one cipher is required
	numCiphers := len(input) / 128
	if numCiphers == 0 {
		return nil, fmt.Errorf("at least one cipher is required")
	}

	// Parse all ciphers
	ciphers := make([]crypto.CipherText, numCiphers)
	for i := 0; i < numCiphers; i++ {
		offset := i * 128

		// Parse the first point (left)
		xLeft := big.NewInt(0).SetBytes(input[offset : offset+32])
		yLeft := big.NewInt(0).SetBytes(input[offset+32 : offset+64])
		leftPoint, err := crypto.ConvertBig2AffinePoint(xLeft, yLeft)
		if err != nil {
			return nil, fmt.Errorf("invalid left point for cipher %d: %v", i, err)
		}

		// Parse the second point (right)
		xRight := big.NewInt(0).SetBytes(input[offset+64 : offset+96])
		yRight := big.NewInt(0).SetBytes(input[offset+96 : offset+128])
		rightPoint, err := crypto.ConvertBig2AffinePoint(xRight, yRight)
		if err != nil {
			return nil, fmt.Errorf("invalid right point for cipher %d: %v", i, err)
		}

		ciphers[i] = crypto.CipherText{
			Left:  leftPoint,
			Right: rightPoint,
		}
	}

	// Perform multiple cipher addition
	left, right, err := crypto.AddMultipleElGamal(ciphers)
	if err != nil {
		return nil, fmt.Errorf("multiple ElGamal addition failed: %v", err)
	}

	// Convert results to bytes
	leftBytes := crypto.ConvertAffinePoint2Bytes(left)
	rightBytes := crypto.ConvertAffinePoint2Bytes(right)

	result := make([]byte, 0, len(leftBytes)+len(rightBytes))
	result = append(result, leftBytes...)
	result = append(result, rightBytes...)
	return result, nil
}
