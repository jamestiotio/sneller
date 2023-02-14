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

// Package testquery provides common functions used in query tests.
package testquery

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/SnellerInc/sneller/date"
	"github.com/SnellerInc/sneller/expr"
	"github.com/SnellerInc/sneller/expr/partiql"
	"github.com/SnellerInc/sneller/ion"
	"github.com/SnellerInc/sneller/ion/blockfmt"
	"github.com/SnellerInc/sneller/ion/zion"
	"github.com/SnellerInc/sneller/plan"
	"github.com/SnellerInc/sneller/tests"
	"github.com/SnellerInc/sneller/vm"
)

type Bufhandle []byte

func (b Bufhandle) Open(_ context.Context) (vm.Table, error) {
	return vm.BufferTable([]byte(b), len(b)), nil
}

func (b Bufhandle) Encode(dst *ion.Buffer, st *ion.Symtab) error {
	return fmt.Errorf("unexpected bufhandle.Encode")
}

func (b Bufhandle) Size() int64 {
	return int64(len(b))
}

type chunkshandle struct {
	chunks [][]byte
	fields []string
}

func (c *chunkshandle) Open(_ context.Context) (vm.Table, error) {
	return c, nil
}

func (c *chunkshandle) Size() int64 {
	n := int64(0)
	for i := range c.chunks {
		n += int64(len(c.chunks[i]))
	}
	return n
}

func (c *chunkshandle) Encode(dst *ion.Buffer, st *ion.Symtab) error {
	return fmt.Errorf("unexpected chunkshandle.Encode")
}

func (c *chunkshandle) writeZion(dst io.WriteCloser) error {
	var mem []byte
	var err error
	var enc zion.Encoder
	for i := range c.chunks {
		mem, err = enc.Encode(c.chunks[i], mem[:0])
		if err != nil {
			dst.Close()
			return err
		}
		_, err = dst.Write(mem)
		if err != nil {
			dst.Close()
			return err
		}
	}
	if shw, ok := dst.(vm.EndSegmentWriter); ok {
		shw.EndSegment()
	} else {
		dst.Close()
		return fmt.Errorf("%T not an EndSegmentWriter?", dst)
	}
	return dst.Close()
}

func (c *chunkshandle) WriteChunks(dst vm.QuerySink, parallel int) error {
	w, err := dst.Open()
	if err != nil {
		return err
	}
	if zw, ok := dst.(blockfmt.ZionWriter); ok && zw.ConfigureZion(c.fields) {
		return c.writeZion(w)
	}
	tmp := vm.Malloc()
	defer vm.Free(tmp)
	for _, buf := range c.chunks {
		if len(buf) > len(tmp) {
			return fmt.Errorf("chunk len %d > PageSize", len(buf))
		}
		for i := range tmp {
			tmp[i] = byte(i & 255)
		}
		size := copy(tmp, buf)
		_, err = w.Write(tmp[:size:size])
		if err != nil {
			closerr := w.Close()
			if errors.Is(err, io.EOF) {
				return closerr
			}
			return err
		}
	}
	if shw, ok := w.(vm.EndSegmentWriter); ok {
		shw.EndSegment()
	} else {
		w.Close()
		return fmt.Errorf("%T not an EndSegmentWriter?", w)
	}
	return w.Close()
}

type parallelchunks struct {
	chunks [][]byte
	fields []string
}

func (p *parallelchunks) Encode(dst *ion.Buffer, st *ion.Symtab) error {
	return fmt.Errorf("unexpected parallelchunks.Encode")
}

func (p *parallelchunks) Size() int64 {
	n := int64(0)
	for i := range p.chunks {
		n += int64(len(p.chunks[i]))
	}
	return n
}

func (p *parallelchunks) SplitBy(parts []string) ([]plan.TablePart, error) {
	type part struct {
		keys, values []ion.Datum
	}

	okType := func(d ion.Datum) bool {
		switch d.Type() {
		case ion.StringType, ion.IntType, ion.UintType, ion.FloatType:
			return true
		default:
			return false
		}
	}

	partset := make(map[string]*part)

	var st ion.Symtab
	var tmp ion.Buffer
	var d ion.Datum
	var err error
	for i := range p.chunks {
		buf := p.chunks[i]
		for len(buf) > 0 {
			d, buf, err = ion.ReadDatum(&st, buf)
			if err != nil {
				panic(err)
			}
			s, err := d.Struct()
			if err != nil {
				panic(err)
			}
			var keys []ion.Datum
			for _, part := range parts {
				f, ok := s.FieldByName(part)
				if !ok {
					return nil, fmt.Errorf("record missing %q field", part)
				}
				if !okType(f.Datum) {
					return nil, fmt.Errorf("value for %q has unacceptable type", part)
				}
				keys = append(keys, f.Datum)
			}
			tmp.Reset()
			for _, key := range keys {
				key.Encode(&tmp, &st)
			}
			if p := partset[string(tmp.Bytes())]; p != nil {
				p.values = append(p.values, d)
			} else {
				partset[string(tmp.Bytes())] = &part{
					keys:   keys,
					values: []ion.Datum{d},
				}
			}
		}
	}

	ret := make([]plan.TablePart, 0, len(partset))
	for _, v := range partset {
		st.Reset()
		tmp.Reset()
		data := flatten(v.values, &st)
		ret = append(ret, plan.TablePart{
			Handle: Bufhandle(data),
			Parts:  v.keys,
		})
	}
	return ret, nil
}

/* FIXME: uncomment this once BOOL_AND() is fixed
      so that partitions for which the condition is not satisfied
      do not cause the merged result to short-circuit to FALSE
   func (p *parallelchunks) Split() (plan.Subtables, error) {
	tp := &plan.LocalTransport{Threads: 1}
	if len(p.chunks) == 1 {
		return plan.SubtableList{{Transport: tp, Handle: p}}, nil
	}
	first := p.chunks[:len(p.chunks)/2]
	second := p.chunks[len(p.chunks)/2:]
	return plan.SubtableList{
		{
			Transport: tp,
			Handle: &parallelchunks{chunks: first, fields: p.fields},
		},
		{
			Transport: tp,
			Handle: &parallelchunks{chunks: second, fields: p.fields},
		},
	}, nil
   }
*/

func (p *parallelchunks) Open(_ context.Context) (vm.Table, error) {
	return p, nil
}

func (p *parallelchunks) WriteChunks(dst vm.QuerySink, parallel int) error {
	outputs := make([]io.WriteCloser, len(p.chunks))
	errlist := make([]error, len(p.chunks))
	var wg sync.WaitGroup
	for i := range outputs {
		w, err := dst.Open()
		if err != nil {
			return err
		}
		outputs[i] = w
	}
	wg.Add(len(outputs))
	for i := range outputs {
		go func(w io.WriteCloser, mem []byte, errp *error) {
			defer wg.Done()
			seterr := func(e error) {
				if e != nil && !errors.Is(e, io.EOF) && *errp == nil {
					*errp = e
				}
			}
			var err error
			if zw, ok := w.(blockfmt.ZionWriter); ok && zw.ConfigureZion(p.fields) {
				var enc zion.Encoder
				mem, err = enc.Encode(mem, nil)
				if err != nil {
					seterr(err)
					w.Close()
					return
				}
			}
			_, err = w.Write(mem)
			if err != nil {
				seterr(err)
				w.Close()
				return
			}
			if shw, ok := w.(vm.EndSegmentWriter); ok {
				shw.EndSegment()
			} else {
				seterr(fmt.Errorf("%T not an EndSegmentWriter?", w))
				w.Close()
				return
			}
			seterr(w.Close())
		}(outputs[i], p.chunks[i], &errlist[i])
	}
	wg.Wait()
	for i := range errlist {
		if errlist[i] != nil {
			return errlist[i]
		}
	}
	return nil
}

type Queryenv struct {
	In []plan.TableHandle
}

func (e *Queryenv) handle(t expr.Node) (plan.TableHandle, bool) {
	p, ok := expr.FlatPath(t)
	if !ok {
		return nil, false
	}
	if len(p) != 1 {
		return nil, false
	}
	if p[0] == "input" && len(e.In) == 1 {
		return e.In[0], true
	}
	var i int
	if n, _ := fmt.Sscanf(p[0], "input%d", &i); n > 0 && i >= 0 && i < len(e.In) {
		return e.In[i], true
	}
	return nil, false
}

func setHints(h plan.TableHandle, hints *plan.Hints) {
	switch t := h.(type) {
	case *chunkshandle:
		t.fields = hints.Fields
	case *parallelchunks:
		t.fields = hints.Fields
	}
}

// Stat implements plan.Env.Stat
func (e *Queryenv) Stat(t expr.Node, h *plan.Hints) (plan.TableHandle, error) {
	handle, ok := e.handle(t)
	if !ok {
		return nil, fmt.Errorf("unexpected table expression %q", expr.ToString(t))
	}
	setHints(handle, h)
	return handle, nil
}

var _ plan.Indexer = &Queryenv{}

type handleIndex struct {
	h plan.TableHandle
}

func (h *handleIndex) TimeRange(path []string) (min, max date.Time, ok bool) {
	return // unimplemented for now
}

func (h *handleIndex) HasPartition(x string) bool {
	sh, ok := h.h.(plan.PartitionHandle)
	if ok {
		// XXX very slow:
		_, err := sh.SplitBy([]string{x})
		return err == nil
	}
	return false
}

func (e *Queryenv) Index(t expr.Node) (plan.Index, error) {
	handle, ok := e.handle(t)
	if !ok {
		return nil, fmt.Errorf("unexpected table expression %q", expr.ToString(t))
	}
	return &handleIndex{handle}, nil
}

var _ plan.TableLister = (*Queryenv)(nil)

func (e *Queryenv) ListTables(db string) ([]string, error) {
	if db != "" {
		return nil, fmt.Errorf("no databases")
	}
	if len(e.In) == 1 {
		return []string{"input"}, nil
	}
	ts := make([]string, len(e.In))
	for i := range e.In {
		ts[i] = fmt.Sprintf("input%d", i)
	}
	return ts, nil
}

// ReadTestcase reads a testcase file, that contains three or more
// part separated with '---'.
//
// The first part is an SQL query (text), the last part is the
// expected rows (JSONRL) and the middle parts are inputs (also
// in the JSONRL format).
func ReadTestcase(fname string) (query []byte, inputs [][]byte, output []byte, err error) {

	// stripInlineComment removes comment from **correctly** formated line.
	// For example: stripInlineComment(`{"x": 5} # sample data`) => `{"x": 5}`
	stripInlineComment := func(line []byte) []byte {
		pos := bytes.LastIndexByte(line, '}')
		if pos == -1 {
			return line
		}
		trimmed := bytes.TrimSpace(line[pos+1:])
		if len(trimmed) == 0 || trimmed[0] == '#' {
			return line[:pos+1]
		}
		return line
	}

	spec, err := tests.ReadTestcaseFromFile(fname)
	if err != nil {
		return nil, nil, nil, err
	}

	n := len(spec.Sections)
	if n < 3 {
		return nil, nil, nil, fmt.Errorf("expected at least 3 sections in testcase, got %d", n)
	}

	part2bytes := func(part []string) []byte {
		var res []byte
		for i := range part {
			res = append(res, []byte(part[i])...)
			res = append(res, '\n')
		}

		return res
	}

	jsonrl2bytes := func(part []string) []byte {
		var res []byte
		for i := range part {
			res = append(res, stripInlineComment([]byte(part[i]))...)
			res = append(res, '\n')
		}

		return res
	}

	query = part2bytes(spec.Sections[0])
	output = jsonrl2bytes(spec.Sections[n-1])

	for i := 1; i < n-1; i++ {
		inputs = append(inputs, jsonrl2bytes(spec.Sections[i]))
	}

	return query, inputs, output, nil
}

type benchmarkSettings struct {
	Symbolizeprob float64 // symbolize probability: 0=never, 1=always
}

// ReadBenchmark reads a benchmark specification.
//
// Benchmark may contain either SQL query (text) and sample
// input (JSONRL) separarted with '---'.
//
// Alternatively, it may contain only an SQL query. But
// then the FROM part has to be a string which is treated
// as a filename and this file is read into the input (JSONRL).
func ReadBenchmark(fname string) (*expr.Query, *benchmarkSettings, []byte, error) {
	spec, err := tests.ReadTestcaseFromFile(fname)
	if err != nil {
		return nil, nil, nil, err
	}

	part2bytes := func(part []string) []byte {
		var res []byte
		for i := range part {
			res = append(res, []byte(part[i])...)
			res = append(res, '\n')
		}

		return res
	}

	text := part2bytes(spec.Sections[0])
	query, err := partiql.Parse(text)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("cannot parse %q: %s", text, err)
	}

	var input []byte
	switch n := len(spec.Sections); n {
	case 1: // only query
		sel := query.Body.(*expr.Select)
		table := sel.From.(*expr.Table)
		file, ok := table.Expr.(expr.String)
		if !ok {
			return nil, nil, nil, fmt.Errorf("benchark without input part has to refer an external JSONRL file")
		}

		path := filepath.Join(filepath.Dir(fname), string(file))
		f, err := os.Open(path)
		if err != nil {
			return nil, nil, nil, err
		}

		input, err = io.ReadAll(f)
		f.Close()
		if err != nil {
			return nil, nil, nil, err
		}

		// The testing framework expects path 'input'
		table.Expr = expr.Ident("input")

	case 2: // query and inline input
		input = part2bytes(spec.Sections[1])

	default:
		return nil, nil, nil, fmt.Errorf("expected at most two parts of benchmark, got %d", n)
	}

	bs := &benchmarkSettings{
		Symbolizeprob: 0.0,
	}
	v := spec.Tags["symbolize"]
	switch v {
	case "":
		// do nothing
	case "never":
		bs.Symbolizeprob = 0.0
	case "always":
		bs.Symbolizeprob = 1.0
	default:
		p, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("cannot parse symbolize=%q: %s", v, err)
		}
		if p < 0.0 || p > 1.0 {
			return nil, nil, nil, fmt.Errorf("symbolize=%f: wrong value, it has to be float in range [0, 1]", p)
		}
	}

	return query, bs, input, nil
}

func Rows(b []byte, outst *ion.Symtab, symbolize func() bool) ([]ion.Datum, error) {

	var symbolizeRandomly func(d ion.Datum, st *ion.Symtab, symbolize func() bool) ion.Datum
	// walk d and replace 50% of the strings with stringSyms
	symbolizeRandomly = func(d ion.Datum, st *ion.Symtab, symbolize func() bool) ion.Datum {
		switch d.Type() {
		case ion.StructType:
			d, _ := d.Struct()
			fields := d.Fields(nil)
			for i := range fields {
				if str, err := fields[i].String(); err == nil {
					if symbolize() {
						fields[i].Datum = ion.Interned(st, str)
					}
				} else {
					fields[i].Datum = symbolizeRandomly(fields[i].Datum, st, symbolize)
				}
			}
			return ion.NewStruct(st, fields).Datum()
		case ion.ListType:
			d, _ := d.List()
			items := d.Items(nil)
			for i := range items {
				if str, err := items[i].String(); err == nil {
					if symbolize() {
						items[i] = ion.Interned(st, str)
					}
				} else {
					items[i] = symbolizeRandomly(items[i], st, symbolize)
				}
			}
			return ion.NewList(st, items).Datum()
		}
		return d
	}

	parseSpecialFPValues := func(s string) ion.Datum {
		const prefix = "float64:"
		if strings.HasPrefix(s, prefix) {
			s = s[len(prefix):]
			switch s {
			case "inf", "+inf":
				return ion.Float(math.Inf(+1))
			case "-inf":
				return ion.Float(math.Inf(-1))
			case "nan", "NaN":
				return ion.Float(math.NaN())
			case "-0":
				return ion.Float(math.Float64frombits(0x8000_0000_0000_0000))
			default:
				panic(fmt.Sprintf("unknown test function %q", s))
			}
		}
		return ion.Empty
	}

	var insertSpecialFPValues func(d ion.Datum, st *ion.Symtab) ion.Datum
	insertSpecialFPValues = func(d ion.Datum, st *ion.Symtab) ion.Datum {
		switch d.Type() {
		case ion.StructType:
			d, _ := d.Struct()
			fields := d.Fields(nil)
			for i := range fields {
				if str, err := fields[i].String(); err == nil {
					if v := parseSpecialFPValues(str); !v.IsEmpty() {
						fields[i].Datum = v
					}
				} else {
					fields[i].Datum = insertSpecialFPValues(fields[i].Datum, st)
				}
			}
			return ion.NewStruct(st, fields).Datum()
		case ion.ListType:
			d, _ := d.List()
			items := d.Items(nil)
			for i := range items {
				if str, err := items[i].String(); err == nil {
					if v := parseSpecialFPValues(str); !v.IsEmpty() {
						items[i] = v
					}
				} else {
					items[i] = insertSpecialFPValues(items[i], st)
				}
			}
			return ion.NewList(st, items).Datum()
		}
		return d
	}

	if len(b) == 0 {
		return nil, nil
	}
	d := json.NewDecoder(bytes.NewReader(b))
	var lst []ion.Datum
	for {
		d, err := ion.FromJSON(outst, d)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		d = insertSpecialFPValues(d, outst)
		if symbolize != nil {
			d = symbolizeRandomly(d, outst, symbolize)
		}
		lst = append(lst, d)
	}
	return lst, nil
}

func flatten(lst []ion.Datum, st *ion.Symtab) []byte {
	var outbuf ion.Buffer
	for i := range lst {
		lst[i].Encode(&outbuf, st)
	}
	tail := outbuf.Bytes()
	outbuf.Set(nil)
	st.Marshal(&outbuf, true)
	outbuf.UnsafeAppend(tail)
	return outbuf.Bytes()
}

type RunFlags int

const (
	FlagParallel    = 1 << iota // run in parallel
	FlagResymbolize             // resymbolize
	FlagShuffle                 // shuffle symbol table
	FlagSplit                   // use a split plan
)

const Shufflecount = 10

func toJSON(st *ion.Symtab, d ion.Datum) string {
	if d.IsEmpty() {
		return "<nil>"
	}
	var ib ion.Buffer
	ib.StartChunk(st)
	d.Encode(&ib, st)
	br := bufio.NewReader(bytes.NewReader(ib.Bytes()))
	var sb strings.Builder
	_, err := ion.ToJSON(&sb, br)
	if err != nil {
		panic(err)
	}
	return sb.String()
}

func ExecuteQuery(q *expr.Query, st *ion.Symtab, in [][]ion.Datum, out []ion.Datum, flags RunFlags) (err error) {

	// fix up the symbols in lst so that they
	// match the associated symbols in st
	fixup := func(lst []ion.Datum, st *ion.Symtab) {
		// we reset the symbol elements of structure fields
		// inside Encode, so the easiest way to do this is
		// just encode the data and throw it away
		var dummy ion.Buffer
		for i := range lst {
			lst[i].Encode(&dummy, st)
			dummy.Reset()
		}
	}

	// run a query on the given input table and yield the output list
	run := func(q *expr.Query, in [][]ion.Datum, st *ion.Symtab, flags RunFlags) ([]ion.Datum, error) {
		var unsymbolize func(d ion.Datum, st *ion.Symtab) ion.Datum
		unsymbolize = func(d ion.Datum, st *ion.Symtab) ion.Datum {
			switch d.Type() {
			case ion.StructType:
				d, _ := d.Struct()
				fields := d.Fields(nil)
				for i := range fields {
					if str, err := fields[i].String(); err == nil {
						fields[i].Datum = ion.String(str)
					} else {
						fields[i].Datum = unsymbolize(fields[i].Datum, st)
					}
				}
				return ion.NewStruct(st, fields).Datum()
			case ion.ListType:
				d, _ := d.List()
				items := d.Items(nil)
				for i := range items {
					if str, err := items[i].String(); err == nil {
						items[i] = ion.String(str)
					} else {
						items[i] = unsymbolize(items[i], st)
					}
				}
				return ion.NewList(st, items).Datum()
			}
			return d
		}

		// return a symbol table with the symbols
		// randomly shuffled
		shuffled := func(st *ion.Symtab) *ion.Symtab {
			ret := &ion.Symtab{}
			// if only one symbol is in the input corpus,
			// then just bump it up one symbol
			if st.MaxID() == 11 {
				ret.Intern("a-random-symbol")
				ret.Intern(st.Get(11))
				return ret
			}

			// first 10 symbols are "pre-interned"
			symbolmap := make([]ion.Symbol, st.MaxID()-10)
			for i := range symbolmap {
				symbolmap[i] = ion.Symbol(i) + 10
			}
			rand.Shuffle(len(symbolmap), func(i, j int) {
				symbolmap[i], symbolmap[j] = symbolmap[j], symbolmap[i]
			})

			// force symbols to be multi-byte sequences:
			for i := 0; i < 117; i++ {
				ret.Intern(fmt.Sprintf("..garbage%d", i))
			}

			for _, s := range symbolmap {
				ret.Intern(st.Get(s))
			}
			return ret
		}

		input := make([]plan.TableHandle, len(in))
		maxp := 0
		for i, in := range in {
			if (flags&FlagResymbolize) != 0 && len(in) > 1 {
				half := len(in) / 2
				first := flatten(in[:half], st)

				var second []byte
				if flags&FlagShuffle != 0 {
					second = flatten(in[half:], shuffled(st))
				} else {
					second = flatten(in[half:], st)
				}
				if flags&FlagParallel != 0 {
					maxp = 2
					input[i] = &parallelchunks{chunks: [][]byte{first, second}}
				} else {
					input[i] = &chunkshandle{chunks: [][]byte{first, second}}
				}
			} else {
				input[i] = Bufhandle(flatten(in, st))
			}
		}
		env := &Queryenv{In: input}
		var tree *plan.Tree
		var err error
		if flags&FlagSplit != 0 {
			tree, err = plan.NewSplit(q, env)
		} else {
			tree, err = plan.New(q, env)
		}
		if err != nil {
			return nil, err
		}
		var out bytes.Buffer
		lp := plan.LocalTransport{Threads: maxp}
		params := plan.ExecParams{
			Output:   &out,
			Parallel: maxp,
			Context:  context.Background(),
		}
		err = lp.Exec(tree, &params)
		if err != nil {
			return nil, err
		}
		outbuf := out.Bytes()
		var datum ion.Datum
		var outlst []ion.Datum
		st.Reset()
		for len(outbuf) > 0 {
			datum, outbuf, err = ion.ReadDatum(st, outbuf)
			if err != nil {
				return nil, err
			}
			datum = unsymbolize(datum, st)
			outlst = append(outlst, datum)
		}
		return outlst, nil
	}

	gotout, err := run(q, in, st, flags)
	if err != nil {
		return err
	}
	st.Reset()
	fixup(gotout, st)
	fixup(out, st)
	if len(out) != len(gotout) {
		return fmt.Errorf("%d rows out; expected %d", len(gotout), len(out))
	}
	nErrors := 0
	for i := range out {
		if i >= len(gotout) {
			err = errors.Join(err, fmt.Errorf("missing %s", toJSON(st, out[i])))
			continue
		}
		if !ion.Equal(out[i], gotout[i]) {
			nErrors++
			err = errors.Join(err, fmt.Errorf("\nrow %d: got  %srow %d: want %s\"",
				i, toJSON(st, gotout[i]), i, toJSON(st, out[i])))
		}
		if nErrors > 10 {
			err = errors.Join(err, fmt.Errorf("... and more nErrors"))
			break
		}
	}
	return err
}