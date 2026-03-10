package precompiled

import (
	"fmt"
	"github.com/0xPolygon/polygon-edge/types"
	"math/big"
	"testing"
	"time"
)

func TestElGamalAdd(t *testing.T) {
	hexs := []string{
		"33c2cdbaf39c3d0e2365f5aeb88da4e42920ba09e25727682296af384f8466e",
		"28754b48dbdaef74f569bd85396099fdd713a709fdb077dac7aa30aeeae270d0",
		"71a5643599dc438a24ffec07da69542f7579a4c487ad2c9711b06507a0ee8f3",
		"8732062779187ffe5767f6025b47ef41ea9251dfd01d2ef019eb0675e82e734",
		"e0f2064d7dcbe663b28fa96dbe902c251d851670780861bd8385a13bc20d29e",
		"25ed5ebf7e085efc66d7dc4178ed77cd3ee2dfb3c8f019bcd7cc79ebb94ce195",
		"994f722394794acba71235a624a7eada3e971882d2dd74d253d471c439ecf3",
		"6a4bb175d31b7772d39674bcb3206ed2a689f4ca6ca12e367095238d20d1c54",
	}
	var bytes []byte
	for _, s := range hexs {
		big, _ := big.NewInt(0).SetString(s, 16)
		b := BigIntTo32BytesBE(big)
		bytes = append(bytes, b...)
	}

	before := time.Now()
	e := elGamalAdd{}
	e.run(bytes, types.Address{}, nil)

	duration := time.Now().Sub(before)
	ns := duration.Nanoseconds()
	fmt.Printf("Duration: %d ns\n", ns)
}

func BigIntTo32BytesBE(x *big.Int) []byte {
	b := x.Bytes()           // big-endian bytes
	res := make([]byte, 32)  // 32-byte slice initialized with zeros
	copy(res[32-len(b):], b) // right-align the big.Int bytes
	return res
}
