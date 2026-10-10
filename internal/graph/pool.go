package graph

import (
	"errors"
	"sync/atomic"
)

// ErrSaturated means every search buffer is in use.
var ErrSaturated = errors.New("search pool saturated")

// Pool is a fixed set of search buffers. Acquiring never blocks.
type Pool struct {
	buffers chan *buffer
	nNodes  int
	inUse   atomic.Int64
}

// NewPool allocates size buffers for graphs of up to nNodes nodes.
func NewPool(size, nNodes int) *Pool {
	p := &Pool{buffers: make(chan *buffer, size), nNodes: nNodes}
	for range size {
		p.buffers <- newBuffer(nNodes)
	}
	return p
}

// InUse is the number of buffers taken by running searches.
func (p *Pool) InUse() int {
	return int(p.inUse.Load())
}

// Size is the number of buffers in the pool.
func (p *Pool) Size() int {
	return cap(p.buffers)
}

func (p *Pool) acquire() (*buffer, error) {
	select {
	case b := <-p.buffers:
		p.inUse.Add(1)
		return b, nil
	default:
		return nil, ErrSaturated
	}
}

func (p *Pool) release(b *buffer) {
	p.inUse.Add(-1)
	p.buffers <- b
}

// buffer holds the per-node state of one search. A node's dur, dist and pred
// are valid only when its stamp equals gen, so a new search does not clear O(n).
type buffer struct {
	dur   []int64
	dist  []int64
	pred  []int32
	stamp []uint32
	done  []uint32
	gen   uint32
	heap  minHeap
}

func newBuffer(n int) *buffer {
	return &buffer{
		dur:   make([]int64, n),
		dist:  make([]int64, n),
		pred:  make([]int32, n),
		stamp: make([]uint32, n),
		done:  make([]uint32, n),
	}
}

func (b *buffer) reset() {
	b.gen++
	if b.gen == 0 {
		clear(b.stamp)
		clear(b.done)
		b.gen = 1
	}
	b.heap = b.heap[:0]
}

func (b *buffer) seen(v int32) bool {
	return b.stamp[v] == b.gen
}

func (b *buffer) settled(v int32) bool {
	return b.done[v] == b.gen
}

type heapItem struct {
	dur  int64
	dist int64
	node int32
}

func (a heapItem) less(b heapItem) bool {
	if a.dur != b.dur {
		return a.dur < b.dur
	}
	return a.dist < b.dist
}

// minHeap is a binary heap ordered by duration, then distance.
type minHeap []heapItem

func (h *minHeap) push(it heapItem) {
	*h = append(*h, it)
	s := *h
	i := len(s) - 1
	for i > 0 {
		parent := (i - 1) / 2
		if !s[i].less(s[parent]) {
			break
		}
		s[i], s[parent] = s[parent], s[i]
		i = parent
	}
}

func (h *minHeap) pop() heapItem {
	s := *h
	top := s[0]
	last := len(s) - 1
	s[0] = s[last]
	s = s[:last]
	i := 0
	for {
		l := 2*i + 1
		if l >= len(s) {
			break
		}
		small := l
		if r := l + 1; r < len(s) && s[r].less(s[l]) {
			small = r
		}
		if !s[small].less(s[i]) {
			break
		}
		s[i], s[small] = s[small], s[i]
		i = small
	}
	*h = s
	return top
}
