package pengine

// RingBuffer is a circular buffer of func() tasks that doubles in capacity when full.
type RingBuffer[T any] struct {
	initialCap int
	buf        []T
	head       int // index of next dequeue; tail = (head + len) % cap
	len        int
}

// NewRingBuffer creates a new RingBuffer with the specified initial capacity.
func NewRingBuffer[T any](cap int) *RingBuffer[T] {
	return &RingBuffer[T]{
		buf:        make([]T, cap),
		initialCap: cap,
	}
}

func (r *RingBuffer[T]) IsEmpty() bool {
	return r.len == 0
}

// Len returns the number of elements currently in the buffer.
func (r *RingBuffer[T]) Len() int {
	return r.len
}

// Push adds a task to the end of the buffer, growing it if necessary.
func (r *RingBuffer[T]) Push(task T) {
	if r.len == len(r.buf) {
		r.grow()
	}

	r.buf[(r.head+r.len)%len(r.buf)] = task
	r.len++
}

// Pop removes and returns the task at the front of the buffer, shrinking it if underutilized.
func (r *RingBuffer[T]) Pop() T {
	var zero T

	task := r.buf[r.head]
	r.buf[r.head] = zero // release reference
	r.head = (r.head + 1) % len(r.buf)
	r.len--

	if cap := len(r.buf); cap > r.initialCap && r.len < cap/4 {
		r.shrink()
	}

	return task
}

// shrink halves the buffer capacity and re-linearises existing elements.
func (r *RingBuffer[T]) shrink() {
	r.resize(max(len(r.buf)/2, r.initialCap))
}

// grow doubles the buffer capacity and re-linearises existing elements.
func (r *RingBuffer[T]) grow() {
	r.resize(len(r.buf) * 2)
}

// resize allocates a new buffer of the given capacity and copies elements into it.
func (r *RingBuffer[T]) resize(newCap int) {
	newBuf := make([]T, newCap)
	tail := (r.head + r.len) % len(r.buf)

	if r.head < tail {
		copy(newBuf, r.buf[r.head:tail])
	} else {
		n := copy(newBuf, r.buf[r.head:])
		copy(newBuf[n:], r.buf[:tail])
	}

	r.head = 0
	r.buf = newBuf
}
