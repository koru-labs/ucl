package precompiled

import (
	"fmt"
	"github.com/0xPolygon/polygon-edge/chain"
	crypto "github.com/0xPolygon/polygon-edge/helper/elgamal"
	"github.com/0xPolygon/polygon-edge/state/runtime"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/consensys/gnark-crypto/ecc/bn254/twistededwards"
	"log"
	"math/big"
)

type elGamalAdd struct{}

func (c *elGamalAdd) gas(input []byte, _ *chain.ForksInTime) uint64 {
	return 21000
}

func (c *elGamalAdd) run(input []byte, caller types.Address, host runtime.Host) ([]byte, error) {
	log.Printf("elGamalAdd is called with input length: %d", len(input))
	if len(input) != 256 {
		return nil, fmt.Errorf("input must be 256 bytes")
	}

	points := make([]*twistededwards.PointAffine, 4)
	for i := 0; i < 4; i++ {
		offset := i * 64
		x := big.NewInt(0).SetBytes(input[offset : offset+32])
		y := big.NewInt(0).SetBytes(input[offset+32 : offset+64])

		point, err := crypto.ConvertBig2AffinePoint(x, y)
		if err != nil {
			_ = fmt.Errorf("invalid points[%d] point: %v", i, err)
			return nil, err
		}
		points[i] = point
	}

	left, right, err := crypto.AddElGamal(points[0], points[1], points[2], points[3])
	if err != nil {
		return nil, fmt.Errorf("ElGamal addition failed: %v", err)
	}

	leftBytes := crypto.ConvertAffinePoint2Bytes(left)
	rightBytes := crypto.ConvertAffinePoint2Bytes(right)

	result := make([]byte, 0, len(leftBytes)+len(rightBytes))
	result = append(result, leftBytes...)
	result = append(result, rightBytes...)
	return result, nil
}

func printElGamaAmount(left *twistededwards.PointAffine, right *twistededwards.PointAffine) {
	var (
		leftXBig  big.Int
		leftYBig  big.Int
		rightXBig big.Int
		rightYBig big.Int
	)
	left.X.ToBigIntRegular(&leftXBig)
	left.Y.ToBigIntRegular(&leftYBig)
	right.X.ToBigIntRegular(&rightXBig)
	right.Y.ToBigIntRegular(&rightYBig)

	println(leftXBig.Text(16))
	println(leftYBig.Text(16))
	println(rightXBig.Text(16))
	println(rightYBig.Text(16))
}
