package jsonrpc

import (
	"errors"
)

var errResponseTooLarge = errors.New("response too large")

// limitedWriter accepts writes until limit bytes have been buffered, then
// returns errResponseTooLarge so encoding can stop mid-stream.
type limitedWriter struct {
	buf   []byte
	n     uint64
	limit uint64
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if w.limit != 0 && w.n+uint64(len(p)) > w.limit {
		return 0, errResponseTooLarge
	}

	w.buf = append(w.buf, p...)
	w.n += uint64(len(p))

	return len(p), nil
}

// marshalCapped serialises v, aborting during encoding if the output would
// exceed limit bytes. A limit of 0 disables the cap.
func marshalCapped(v interface{}, limit uint64) ([]byte, error) {
	if limit == 0 {
		return fastJSONIt.Marshal(v)
	}

	w := &limitedWriter{limit: limit}
	stream := fastJSONIt.BorrowStream(w)

	defer fastJSONIt.ReturnStream(stream)

	stream.WriteVal(v)

	if stream.Error != nil {
		return nil, stream.Error
	}

	if err := stream.Flush(); err != nil {
		return nil, err
	}

	return w.buf, nil
}
