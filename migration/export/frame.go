package export

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// A frame is how a record travels and is cached: the CRC-32C of the record's RLP (4 bytes, big
// endian), then the RLP compressed with zstd. The checksum catches a record damaged on disk or in
// transit; whether its content is ucl's is checked by the follower against ucl's signed headers.

var (
	castagnoli = crc32.MakeTable(crc32.Castagnoli)

	encoderOnce sync.Once
	encoder     *zstd.Encoder
	decoderOnce sync.Once
	decoder     *zstd.Decoder

	errChecksum = errors.New("record checksum mismatch")
)

func zstdEncoder() *zstd.Encoder {
	encoderOnce.Do(func() {
		// No concurrency inside one record: ranges compress records on several workers already.
		encoder, _ = zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	})

	return encoder
}

func zstdDecoder() *zstd.Decoder {
	decoderOnce.Do(func() {
		decoder, _ = zstd.NewReader(nil, zstd.WithDecoderConcurrency(0))
	})

	return decoder
}

// EncodeFrame frames a record's RLP.
func EncodeFrame(rlp []byte) []byte {
	frame := binary.BigEndian.AppendUint32(nil, crc32.Checksum(rlp, castagnoli))

	return zstdEncoder().EncodeAll(rlp, frame)
}

// DecodeFrame returns the record RLP a frame holds, refusing it if the checksum does not match.
func DecodeFrame(frame []byte) ([]byte, error) {
	if len(frame) < 4 {
		return nil, fmt.Errorf("%w: frame of %d bytes", errChecksum, len(frame))
	}

	rlp, err := zstdDecoder().DecodeAll(frame[4:], nil)
	if err != nil {
		return nil, err
	}

	if crc32.Checksum(rlp, castagnoli) != binary.BigEndian.Uint32(frame[:4]) {
		return nil, errChecksum
	}

	return rlp, nil
}
