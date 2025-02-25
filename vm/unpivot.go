// Copyright (C) 2022 Sneller, Inc.
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package vm

import (
	"bytes"
	"io"
	"math/bits"
	"sync/atomic"
	"unsafe"

	"github.com/SnellerInc/sneller/internal/aes"
	"github.com/SnellerInc/sneller/internal/atomicext"
	"github.com/SnellerInc/sneller/ints"
	"github.com/SnellerInc/sneller/ion"
	"golang.org/x/exp/slices"
)

const (
	outRowsCapacity      = 1024 // The size of a buffer for the metadata describing individual rows
	simdChunkBits   uint = 512
)

var dummyVMRef vmref = vmref{0, 0}

type creatorFunc func(u *Unpivot, w io.WriteCloser) rowConsumer

type Unpivot struct {
	out      QuerySink
	as       *string
	at       *string
	fnCreate creatorFunc
}

func createKernelUnpivotAsAt(u *Unpivot, w io.WriteCloser) rowConsumer {
	return &kernelUnpivotAsAt{
		kernelUnpivotBase: kernelUnpivotBase{
			parent: u,
			out:    asRowConsumer(w),
		},
	}
}

func createKernelUnpivotAs(u *Unpivot, w io.WriteCloser) rowConsumer {
	return &kernelUnpivotAs{
		kernelUnpivotBase: kernelUnpivotBase{
			parent: u,
			out:    asRowConsumer(w),
		},
	}
}

func createKernelUnpivotAt(u *Unpivot, w io.WriteCloser) rowConsumer {
	return &kernelUnpivotAt{
		kernelUnpivotBase: kernelUnpivotBase{
			parent: u,
			out:    asRowConsumer(w),
		},
	}
}

// NewUnpivot creates a new Unpivot kernel that unpivots a tuple into a set of pairs, per PartiQL.pdf, $5.2
func NewUnpivot(as *string, at *string, dst QuerySink) (*Unpivot, error) {
	// Select the creator based on the provided labels
	var creator creatorFunc
	if as != nil {
		if at != nil {
			creator = createKernelUnpivotAsAt
		} else {
			creator = createKernelUnpivotAs
		}
	} else {
		if at != nil {
			creator = createKernelUnpivotAt
		} else {
			panic("'as' and 'at' cannot both be nil") // should have been validated before, double-checking here
		}
	}

	u := &Unpivot{
		out:      dst,
		as:       as,
		at:       at,
		fnCreate: creator,
	}
	return u, nil
}

func (u *Unpivot) Open() (io.WriteCloser, error) {
	w, err := u.out.Open()
	if err != nil {
		return nil, err
	}
	c := u.fnCreate(u, w)
	return splitter(c), nil
}

func (u *Unpivot) Close() error {
	return u.out.Close()
}

type kernelUnpivotBase struct {
	parent   *Unpivot
	out      rowConsumer // The downstream kernel
	params   rowParams
	dummy    []vmref // dummy rows
	syms     *symtab
	aux      *auxbindings
	resolver precedenceResolver
}

func (u *kernelUnpivotBase) Close() error {
	return u.out.Close()
}

func (u *kernelUnpivotBase) next() rowConsumer {
	return u.out
}

// kernelUnpivotAsAt handles the "UNPIVOT AS val AT key" case
type kernelUnpivotAsAt struct {
	kernelUnpivotBase
}

var _ zionConsumer = &kernelUnpivotAsAt{}

func (u *kernelUnpivotAsAt) zionOk() bool { return true }

func (u *kernelUnpivotAsAt) flush() error {
	if len(u.dummy) == 0 {
		return nil
	}
	// ensure that lane-width reads produce zeros for inactive lanes
	u.params.auxbound[0] = sanitizeAux(u.params.auxbound[0], len(u.params.auxbound[0]))
	u.params.auxbound[1] = sanitizeAux(u.params.auxbound[1], len(u.params.auxbound[1]))
	if err := u.out.writeRows(u.dummy, &u.params); err != nil {
		return err
	}
	u.dummy = u.dummy[:0]
	u.params.auxbound[0] = u.params.auxbound[0][:0]
	u.params.auxbound[1] = u.params.auxbound[1][:0]
	return nil
}

func (u *kernelUnpivotAsAt) writeZion(state *zionState) error {
	err := state.buckets.SelectAll()
	if err != nil {
		return err
	}
	// TODO: since state.buckets.Pos gives us 16 starting positions,
	// we can vectorize the processing of the buckets!
	mem := state.buckets.Decompressed
	for len(mem) > 0 {
		sym, rest, err := ion.ReadLabel(mem)
		if err != nil {
			return err
		}
		vsize := ion.SizeOf(rest)
		// add a dummy record with 0 bytes of contents
		// for the "main" row; the rowParams contain
		// the only live bindings after this step
		u.dummy = append(u.dummy, dummyVMRef)
		u.params.auxbound[0] = append(u.params.auxbound[0], u.syms.symrefs[sym])
		restpos, ok := vmdispl(rest)
		if !ok {
			panic("zion decompressed buckets data not in vmm")
		}
		u.params.auxbound[1] = append(u.params.auxbound[1], vmref{restpos, uint32(vsize)})
		if len(u.dummy) == cap(u.dummy) {
			err := u.flush()
			if err != nil {
				return err
			}
		}
		mem = rest[vsize:]
	}
	return u.flush()
}

func (u *kernelUnpivotAsAt) symbolize(st *symtab, aux *auxbindings) error {
	selfaux := auxbindings{}
	selfaux.push(*u.parent.at) // aux 0 = at
	selfaux.push(*u.parent.as) // aux 1 = as
	u.syms = st
	u.aux = aux
	u.params.auxbound = shrink(u.params.auxbound, 2)
	u.params.auxbound[0] = slices.Grow(u.params.auxbound[0][:0], outRowsCapacity)
	u.params.auxbound[1] = slices.Grow(u.params.auxbound[1][:0], outRowsCapacity)
	u.dummy = slices.Grow(u.dummy[:0], outRowsCapacity)
	u.resolver = newPrecedenceResolver(st, aux)
	return u.out.symbolize(st, &selfaux)
}

func (u *kernelUnpivotAsAt) writeRows(rows []vmref, params *rowParams) error {
	// Process the auxilliary bindings first, if provided
	for i, v := range params.auxbound {
		symref := u.resolver.auxrefs[i]
		for len(v) > 0 {
			k := cap(u.dummy) - len(u.dummy)
			if k == 0 {
				// flush; note that the actual row content will be ignored
				if err := u.out.writeRows(u.dummy, &u.params); err != nil {
					return err
				}
				u.dummy = u.dummy[:0]
				u.params.auxbound[0] = u.params.auxbound[0][:0]
				u.params.auxbound[1] = u.params.auxbound[1][:0]
				k = cap(u.dummy)
			}
			// both len(v) > 0 and k > 0 here, then so must be m
			m := ints.Min(k, len(v))
			fillVMrefs(&u.dummy, vmref{0, 0}, m)
			fillVMrefs(&u.params.auxbound[0], symref, m)
			copyVMrefs(&u.params.auxbound[1], &v[0], m)
			v = v[m:]
		}
	}

	// Process the ION input
	for _, x := range rows {
		data := x.mem()
		// Iterate over all the struct fields
		for len(data) != 0 {
			sym, rest, err := ion.ReadLabel(data)
			if err != nil {
				return err
			}
			restsize := ion.SizeOf(rest)
			data = rest[restsize:]

			if !u.resolver.useION(sym) {
				continue
			}

			// add a dummy record with 0 bytes of contents
			// for the "main" row; the rowParams contain
			// the only live bindings after this step
			u.dummy = append(u.dummy, dummyVMRef)
			u.params.auxbound[0] = append(u.params.auxbound[0], u.syms.symrefs[sym])
			restpos, _ := vmdispl(rest)
			u.params.auxbound[1] = append(u.params.auxbound[1], vmref{restpos, uint32(restsize)})

			if len(u.dummy) == cap(u.dummy) {
				err := u.flush()
				if err != nil {
					return err
				}
			}
		}
	}
	return u.flush()
}

// kernelUnpivotAt handles the "UNPIVOT AS val" case
type kernelUnpivotAs struct {
	kernelUnpivotBase
}

func (u *kernelUnpivotAs) symbolize(st *symtab, aux *auxbindings) error {
	selfaux := auxbindings{}
	selfaux.push(*u.parent.as) // aux[0] = as
	u.syms = st
	u.aux = aux
	u.params.auxbound = shrink(u.params.auxbound, 1)
	u.params.auxbound[0] = slices.Grow(u.params.auxbound[0][:0], outRowsCapacity)
	u.dummy = slices.Grow(u.dummy[:0], outRowsCapacity)
	u.resolver = newPrecedenceResolver(st, aux)
	return u.out.symbolize(st, &selfaux)
}

func (u *kernelUnpivotAs) writeRows(rows []vmref, params *rowParams) error {
	// Process the auxilliary bindings first, if provided
	for _, v := range params.auxbound {
		for len(v) > 0 {
			k := cap(u.dummy) - len(u.dummy)
			if k == 0 {
				// flush; note that the actual row content will be ignored
				if err := u.out.writeRows(u.dummy, &u.params); err != nil {
					return err
				}
				u.dummy = u.dummy[:0]
				u.params.auxbound[0] = u.params.auxbound[0][:0]
				k = cap(u.dummy)
			}
			// both len(v) > 0 and k > 0 here, then so must be m
			m := ints.Min(k, len(v))
			fillVMrefs(&u.dummy, vmref{0, 0}, m)
			copyVMrefs(&u.params.auxbound[0], &v[0], m)
			v = v[m:]
		}
	}

	// Process the ION input
	for _, x := range rows {
		data := x.mem()
		// Iterate over all the struct fields
		for len(data) != 0 {
			sym, rest, err := ion.ReadLabel(data)
			if err != nil {
				return err
			}
			restsize := ion.SizeOf(rest)
			data = rest[restsize:] // Seek to the next field of the input ION structure

			if !u.resolver.useION(sym) {
				continue
			}

			u.dummy = append(u.dummy, dummyVMRef)
			vmoff, _ := vmdispl(rest)
			u.params.auxbound[0] = append(u.params.auxbound[0], vmref{vmoff, uint32(restsize)})
			if len(u.params.auxbound) == cap(u.params.auxbound) {
				if err := u.out.writeRows(u.dummy, &u.params); err != nil {
					return err
				}
				u.dummy = u.dummy[:0]
				u.params.auxbound[0] = u.params.auxbound[0][:0]
			}
		}
	}
	if len(u.dummy) > 0 {
		u.params.auxbound[0] = sanitizeAux(u.params.auxbound[0], len(u.params.auxbound[0]))
		if err := u.out.writeRows(u.dummy, &u.params); err != nil {
			return err
		}
		u.dummy = u.dummy[:0]
		u.params.auxbound[0] = u.params.auxbound[0][:0]
	}
	return nil
}

// kernelUnpivotAt handles the "UNPIVOT AT key" case
type kernelUnpivotAt struct {
	kernelUnpivotBase
}

func (u *kernelUnpivotAt) symbolize(st *symtab, aux *auxbindings) error {
	selfaux := auxbindings{}
	selfaux.push(*u.parent.at) // aux[0] = at
	u.syms = st
	u.aux = aux
	u.params.auxbound = shrink(u.params.auxbound, 1)
	u.params.auxbound[0] = slices.Grow(u.params.auxbound[0][:0], outRowsCapacity)
	u.dummy = slices.Grow(u.dummy[:0], outRowsCapacity)
	u.resolver = newPrecedenceResolver(st, aux)
	return u.out.symbolize(st, &selfaux)
}

func (u *kernelUnpivotAt) writeRows(rows []vmref, params *rowParams) error {
	// Process the auxilliary bindings first, if provided
	for i, v := range params.auxbound {
		symref := u.resolver.auxrefs[i]
		for len(v) > 0 {
			k := cap(u.dummy) - len(u.dummy)
			if k == 0 {
				// flush; note that the actual row content will be ignored
				if err := u.out.writeRows(u.dummy, &u.params); err != nil {
					return err
				}
				u.dummy = u.dummy[:0]
				u.params.auxbound[0] = u.params.auxbound[0][:0]
				k = cap(u.dummy)
			}
			// both len(v) > 0 and k > 0 here, then so must be m
			m := ints.Min(k, len(v))
			fillVMrefs(&u.dummy, vmref{0, 0}, m)
			fillVMrefs(&u.params.auxbound[0], symref, m)
			v = v[m:]
		}
	}

	// Process the ION input
	for _, x := range rows {
		data := x.mem()
		// Iterate over all the struct fields
		for len(data) != 0 {
			sym, rest, err := ion.ReadLabel(data)
			if err != nil {
				return err
			}
			restsize := ion.SizeOf(rest)
			data = rest[restsize:] // Seek to the next field of the input ION structure

			if !u.resolver.useION(sym) {
				continue
			}

			u.dummy = append(u.dummy, dummyVMRef)
			u.params.auxbound[0] = append(u.params.auxbound[0], u.syms.symrefs[sym])

			if len(u.dummy) == cap(u.dummy) {
				if err := u.out.writeRows(u.dummy, &u.params); err != nil {
					return err
				}
				u.dummy = u.dummy[:0]
				u.params.auxbound[0] = u.params.auxbound[0][:0]
			}
		}
	}
	if len(u.dummy) > 0 {
		u.params.auxbound[0] = sanitizeAux(u.params.auxbound[0], len(u.params.auxbound[0]))
		if err := u.out.writeRows(u.dummy, &u.params); err != nil {
			return err
		}
		u.dummy = u.dummy[:0]
		u.params.auxbound[0] = u.params.auxbound[0][:0]
	}
	return nil
}

type randomTreeUnifierNode struct {
	link [2]unsafe.Pointer
	hash uint64
	data []byte
}

// randomTreeUnifier eliminates in a lock-free way duplicate byte arrays, storing a single
// unique instance of the input data in a random tree data structure of my own invention.
// The tree is a regular BST with the following properties:
//
//  1. A composite key is used. First the hash is compared, and -- only on a match -- the actual
//     data (in lexicographic order). Tree traversal is therefore very cheap (collisions are
//     extremely unlikely, so typically there is just one full data compare necessary to resolve
//     the matching.
//  2. As there is a secondary comparator, the tree handles colliding hashes just fine without
//     compromising data integrity.
//  3. The semantics of the unifier assures that new keys can only be added. There is no rebalancing:
//     the tree can only grow downwards by inserting new leaf nodes, so all the already valid paths
//     from the root remain valid, no matter how many new nodes have been inserted. This property
//     immediately enables lock-free concurrent insertion from multiple cores with a very efficient
//     contention-handling restart protocol.
//  4. The better the hash function, the more uniform its output distribution is. Under the
//     generally accepted one-way hash function existence assumption, it is impossible
//     to distinguish between a strong hash function and a truly random stream. Per
//     https://en.wikipedia.org/wiki/Random_binary_tree#The_longest_path, this implies
//     a very strong statistical cap of ~4.3log(N) on the height of the tree, making rotations
//     unnecessary. In practice it is even better than that: the height rarely exceeds ~2log(N),
//     putting random trees on par with red-black trees without the added complexity and inherently
//     serial nature of the latter. It suffices to provide a hash function that is good enough.

type randomTreeUnifier struct {
	root unsafe.Pointer
}

func newRandomTreeUnifier() randomTreeUnifier {
	return randomTreeUnifier{}
}

func load(p *unsafe.Pointer) *randomTreeUnifierNode {
	return (*randomTreeUnifierNode)(atomic.LoadPointer(p))
}

func (u *randomTreeUnifier) unify(data []byte) bool {
	h := aes.HashSlice(&aes.Volatile, data)
	var p *randomTreeUnifierNode
	ip := &u.root // insertion point
	for {
		if q := load(ip); q != nil {
			if q.hash == h {
				c := bytes.Compare(data, q.data)
				if c == 0 {
					// a matching node already exists
					return false
				} else {
					// a hash collision
					ip = &q.link[ints.BoolTo[uint](c > 0)]
				}
			} else {
				// hash mismatch
				ip = &q.link[ints.BoolTo[uint](h > q.hash)]
			}
		} else {
			// an empty insertion point has been found
			if p == nil {
				// deferred this expensive step for as long as possible
				buf := make([]byte, len(data))
				copy(buf, data)
				p = &randomTreeUnifierNode{hash: h, data: buf}
			}
			if atomic.CompareAndSwapPointer(ip, nil, unsafe.Pointer(p)) {
				// insertion succeeded
				return true
			}
			// insertion failed: either ip is no longer nil or a spurious CAS failure.
			// Retry, as the current insertion path prefix remains valid due to the grow-only nature of the tree.
			atomicext.Pause()
		}
	}
}

type UnpivotAtDistinct struct {
	out     QuerySink
	at      string
	unifier randomTreeUnifier
}

// NewUnpivotAtDistinct creates a new UnpivotAtDistinct kernel that returns the list of pairs describing the encountered columns
func NewUnpivotAtDistinct(at string, dst QuerySink) (*UnpivotAtDistinct, error) {
	u := &UnpivotAtDistinct{
		out:     dst,
		at:      at,
		unifier: newRandomTreeUnifier(),
	}
	return u, nil
}

func (u *UnpivotAtDistinct) Open() (io.WriteCloser, error) {
	w, err := u.out.Open()
	if err != nil {
		return nil, err
	}
	k := &kernelUnpivotAtDistinct{
		parent: u,
		out:    asRowConsumer(w),
	}
	return splitter(k), nil
}

func (u *UnpivotAtDistinct) Close() error {
	return u.out.Close()
}

// kernelUnpivotAtDistinct handles the "UNPIVOT AT key GROUP BY key" case
type kernelUnpivotAtDistinct struct {
	parent   *UnpivotAtDistinct
	buf      []uint
	out      rowConsumer // The downstream kernel
	params   rowParams
	dummy    []vmref // dummy rows
	syms     *symtab
	aux      *auxbindings
	resolver precedenceResolver
}

func (u *kernelUnpivotAtDistinct) symbolize(st *symtab, aux *auxbindings) error {
	selfaux := auxbindings{}
	selfaux.push(u.parent.at) // aux[0] = at
	u.aux = aux
	u.syms = st
	u.params.auxbound = shrink(u.params.auxbound, 1)
	u.params.auxbound[0] = slices.Grow(u.params.auxbound[0][:0], outRowsCapacity)
	u.dummy = slices.Grow(u.dummy[:0], outRowsCapacity)
	u.resolver = newPrecedenceResolver(st, aux)
	maxID := uint(st.MaxID())
	chunkCount := ints.ChunkCount(maxID, simdChunkBits) // The number of full SIMD registers, not the scalar ones!
	u.buf = make([]uint, chunkCount*(simdChunkBits/bits.UintSize))
	return u.out.symbolize(st, &selfaux)
}

func (u *kernelUnpivotAtDistinct) Close() error {
	return u.out.Close()
}

func (u *kernelUnpivotAtDistinct) next() rowConsumer {
	return u.out
}

//go:noescape
//go:nosplit
func unpivotAtDistinctDeduplicate(rows []vmref, vmbase uintptr, bitvector *uint)

func (u *kernelUnpivotAtDistinct) writeRows(rows []vmref, params *rowParams) error {
	// Mark the auxilliary binding fields first. Duplication with regards
	// to the content of rows is fine, as the next step is deduplication.
	for i, v := range params.auxbound {
		if len(v) > 0 {
			ints.SetBit(u.buf, u.resolver.auxsyms[i])
		}
	}

	// Deduplicate the symbol IDs using a bitvector
	unpivotAtDistinctDeduplicate(rows, vmbase(), &u.buf[0])

	// The field names should remain quite stable across the entire input,
	// hence the result vector is expected to be sparse. This statistics
	// makes the SIMD-accelerated dense vector index rematerialization not
	// worth the trouble.
	for i, v := range u.buf {
		if v == 0 {
			continue // skip empty chunks
		}
		for {
			k := bits.TrailingZeros(v)
			v ^= uint(1) << k
			sym := i*bits.UintSize + k
			ref := u.syms.symrefs[sym]
			if u.parent.unifier.unify(ref.mem()) {
				u.dummy = append(u.dummy, dummyVMRef)
				u.params.auxbound[0] = append(u.params.auxbound[0], ref)

				if len(u.dummy) == cap(u.dummy) {
					if err := u.out.writeRows(u.dummy, &u.params); err != nil {
						return err
					}
					u.dummy = u.dummy[:0]
					u.params.auxbound[0] = u.params.auxbound[0][:0]
				}
			}
			if v == 0 {
				break
			}
		}
	}
	if len(u.dummy) > 0 {
		if err := u.out.writeRows(u.dummy, &u.params); err != nil {
			return err
		}
		u.dummy = u.dummy[:0]
		u.params.auxbound[0] = u.params.auxbound[0][:0]
	}
	return nil
}

type precedenceResolver struct {
	bitmap  []uint
	auxsyms []ion.Symbol
	auxrefs []vmref
}

func newPrecedenceResolver(st *symtab, aux *auxbindings) precedenceResolver {
	// Make sure the names from aux all have their associated vmrefs
	var auxsyms []ion.Symbol
	var auxrefs []vmref
	if aux != nil && len(aux.bound) != 0 {
		n := len(aux.bound)
		auxsyms = make([]ion.Symbol, n)
		auxrefs = make([]vmref, n)
		for i, name := range aux.bound {
			auxsyms[i] = st.Intern(name)
		}
	}

	maxID := uint(st.MaxID())
	chunkCount := ints.ChunkCount(maxID, simdChunkBits) // The number of full SIMD registers, not the scalar ones!
	r := precedenceResolver{
		bitmap:  make([]uint, chunkCount*(simdChunkBits/bits.UintSize)),
		auxsyms: auxsyms,
		auxrefs: auxrefs,
	}

	for i, s := range auxsyms {
		ints.SetBit(r.bitmap, s)
		r.auxrefs[i] = st.symrefs[s]
	}
	return r
}

func (p *precedenceResolver) useAuxilliary(s ion.Symbol) bool {
	return ints.TestBit(p.bitmap, s)
}

func (p *precedenceResolver) useION(s ion.Symbol) bool {
	return !p.useAuxilliary(s)
}

//go:noescape
//go:nosplit
func fillVMrefs(p *[]vmref, v vmref, n int)

//go:noescape
//go:nosplit
func copyVMrefs(p *[]vmref, q *vmref, n int)
