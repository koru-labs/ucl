package crypto

import (
	"fmt"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/consensys/gnark-crypto/ecc/bn254/twistededwards"
	"math/big"
)

func AddElGamal(cipher1Left *twistededwards.PointAffine, cipher1Right *twistededwards.PointAffine, cipher2Left *twistededwards.PointAffine, cipher2Right *twistededwards.PointAffine) (*twistededwards.PointAffine, *twistededwards.PointAffine, error) {
	left := new(twistededwards.PointAffine)
	right := new(twistededwards.PointAffine)
	left.Add(cipher1Left, cipher2Left)
	right.Add(cipher1Right, cipher2Right)
	if left.IsOnCurve() && right.IsOnCurve() {
		return left, right, nil
	}
	return nil, nil, fmt.Errorf("failed to add elgamal")
}

func SubElGamal(cipher1Left *twistededwards.PointAffine, cipher1Right *twistededwards.PointAffine, cipher2Left *twistededwards.PointAffine, cipher2Right *twistededwards.PointAffine) (*twistededwards.PointAffine, *twistededwards.PointAffine, error) {
	left := new(twistededwards.PointAffine)
	right := new(twistededwards.PointAffine)
	left.Neg(cipher2Left)
	right.Neg(cipher2Right)
	left.Add(left, cipher1Left)
	right.Add(right, cipher1Right)
	if left.IsOnCurve() && right.IsOnCurve() {
		return left, right, nil
	}
	return nil, nil, fmt.Errorf("failed to add elgamal")
}

type CipherText struct {
	Left  *twistededwards.PointAffine
	Right *twistededwards.PointAffine
}

func AddMultipleElGamal(ciphers []CipherText) (*twistededwards.PointAffine, *twistededwards.PointAffine, error) {
	// check if there are any ciphers
	if len(ciphers) == 0 {
		return nil, nil, fmt.Errorf("no ciphers provided for addition")
	}

	// initialize result with the first cipher
	resultLeft := new(twistededwards.PointAffine)
	resultRight := new(twistededwards.PointAffine)
	*resultLeft = *ciphers[0].Left
	*resultRight = *ciphers[0].Right

	// check if the first cipher is on the curve
	for i := 1; i < len(ciphers); i++ {
		resultLeft.Add(resultLeft, ciphers[i].Left)
		resultRight.Add(resultRight, ciphers[i].Right)

		// check if the result is on the curve
		if !resultLeft.IsOnCurve() || !resultRight.IsOnCurve() {
			return nil, nil, fmt.Errorf("invalid result after adding cipher %d", i)
		}
	}

	return resultLeft, resultRight, nil
}

func ConvertBig2AffinePoint(xBig *big.Int, yBig *big.Int) (*twistededwards.PointAffine, error) {
	if xBig.Sign() == 0 && yBig.Sign() == 0 {
		fmt.Printf("Converting (0,0) to identity point (0,1)")
		yBig = big.NewInt(1)
	}

	var xFr, yFr fr.Element
	xFr.SetBigInt(xBig)
	yFr.SetBigInt(yBig)

	point := &twistededwards.PointAffine{X: xFr, Y: yFr}
	if !point.IsOnCurve() {
		return nil, fmt.Errorf("point (%s, %s) is not on curve", xBig.String(), yBig.String())
	}
	return point, nil
}

func ConvertAffinePoint2Bytes(point *twistededwards.PointAffine) []byte {
	var bytes []byte
	xBytes := point.X.Bytes()
	yBytes := point.Y.Bytes()
	bytes = append(bytes, xBytes[:]...)
	bytes = append(bytes, yBytes[:]...)
	return bytes
}
