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

package plan

import (
	"errors"
	"fmt"

	"golang.org/x/exp/slices"

	"github.com/SnellerInc/sneller/expr"
	"github.com/SnellerInc/sneller/ion"
	"github.com/SnellerInc/sneller/ion/blockfmt"
	"github.com/SnellerInc/sneller/plan/pir"
	"github.com/SnellerInc/sneller/vm"
)

var (
	ErrNotSupported = errors.New("plan: query not supported")
)

// reject produces an ErrNotSupported error message
func reject(msg string) error {
	return fmt.Errorf("%w: %s", ErrNotSupported, msg)
}

func lowerIterValue(in *pir.IterValue, from Op) (Op, error) {
	return &Unnest{
		Nonterminal: Nonterminal{
			From: from,
		},
		Expr:   in.Value,
		Result: in.Result,
	}, nil
}

func lowerFilter(in *pir.Filter, from Op) (Op, error) {
	return &Filter{
		Nonterminal: Nonterminal{From: from},
		Expr:        in.Where,
	}, nil
}

func lowerDistinct(in *pir.Distinct, from Op) (Op, error) {
	return &Distinct{
		Nonterminal: Nonterminal{From: from},
		Fields:      in.Columns,
	}, nil
}

func lowerLimit(in *pir.Limit, from Op) (Op, error) {
	if in.Count == 0 {
		return NoOutput{}, nil
	}

	// some operations accept Limit natively
	switch f := from.(type) {
	case *HashAggregate:
		f.Limit = int(in.Count)
		if in.Offset != 0 {
			return nil, reject("non-zero OFFSET of hash aggregate result")
		}
		return f, nil
	case *OrderBy:
		f.Limit = int(in.Count)
		f.Offset = int(in.Offset)
		return f, nil
	case *Distinct:
		if in.Offset != 0 {
			return nil, reject("non-zero OFFSET of distinct result")
		}
		f.Limit = in.Count
		return f, nil
	}
	if in.Offset != 0 {
		return nil, reject("OFFSET without GROUP BY/ORDER BY not implemented")
	}
	return &Limit{
		Nonterminal: Nonterminal{From: from},
		Num:         in.Count,
	}, nil
}

func iscountstar(a vm.Aggregation) bool {
	if len(a) != 1 {
		return false
	}

	agg := a[0]
	if agg.Expr.Op != expr.OpCount {
		return false
	}

	if agg.Expr.Filter != nil {
		return false
	}

	_, isstar := agg.Expr.Inner.(expr.Star)
	return isstar
}

func splitWindows(lst vm.Aggregation) (agg vm.Aggregation, window vm.Aggregation) {
	agg = lst[:0]
	for i := range lst {
		if lst[i].Expr.Op.WindowOnly() {
			window = append(window, lst[i])
		} else {
			agg = append(agg, lst[i])
		}
	}
	return agg, window
}

func lowerAggregate(in *pir.Aggregate, from Op) (Op, error) {
	if in.GroupBy == nil {
		// simple aggregate; check for COUNT(*) first
		if iscountstar(in.Agg) {
			return &CountStar{
				Nonterminal: Nonterminal{From: from},
				As:          in.Agg[0].Result,
			}, nil
		}
		return &SimpleAggregate{
			Nonterminal: Nonterminal{From: from},
			Outputs:     in.Agg,
		}, nil
	}
	agg, windows := splitWindows(in.Agg)
	return &HashAggregate{
		Nonterminal: Nonterminal{From: from},
		Agg:         agg,
		Windows:     windows,
		By:          in.GroupBy,
	}, nil
}

func makeOrdering(node expr.Order) vm.SortOrdering {
	var ordering vm.SortOrdering
	if node.Desc {
		ordering.Direction = vm.SortDescending
	} else {
		ordering.Direction = vm.SortAscending
	}

	if node.NullsLast {
		ordering.NullsOrder = vm.SortNullsLast
	} else {
		ordering.NullsOrder = vm.SortNullsFirst
	}

	return ordering
}

func lowerOrder(in *pir.Order, from Op) (Op, error) {
	if ha, ok := from.(*HashAggregate); ok {
		// hash aggregates can accept ORDER BY directly
	outer:
		for i := range in.Columns {
			ex := in.Columns[i].Column
			ordering := makeOrdering(in.Columns[i])

			for col := range ha.Agg {
				if expr.IsIdentifier(ex, ha.Agg[col].Result) {
					ha.OrderBy = append(ha.OrderBy, HashOrder{
						Column:   col,
						Ordering: ordering,
					})
					continue outer
				}
			}
			for col := range ha.By {
				if expr.IsIdentifier(ex, ha.By[col].Result()) {
					ha.OrderBy = append(ha.OrderBy, HashOrder{
						Column:   len(ha.Agg) + col,
						Ordering: ordering,
					})
					continue outer
				}
			}
			for col := range ha.Windows {
				if expr.IsIdentifier(ex, ha.Windows[i].Result) {
					ha.OrderBy = append(ha.OrderBy, HashOrder{
						Column:   len(ha.Agg) + len(ha.By) + col,
						Ordering: ordering,
					})
					continue outer
				}
			}
			// there are cases where we ORDER BY an expression
			// that is composed of multiple aggregate results,
			// and in those cases we cannot merge these operations
			goto slowpath
		}
		return ha, nil
	}

slowpath:
	// ordinary Order node
	columns := make([]vm.SortColumn, 0, len(in.Columns))
	for i := range in.Columns {
		switch in.Columns[i].Column.(type) {
		case expr.Bool, expr.Integer, *expr.Rational, expr.Float, expr.String:
			// skip constant columns; they do not meaningfully apply a sort
			continue
		}

		columns = append(columns, vm.SortColumn{
			Node:     in.Columns[i].Column,
			Ordering: makeOrdering(in.Columns[i]),
		})
	}

	// if we had ORDER BY "foo" or something like that,
	// then we don't need to do any ordering at all
	if len(columns) == 0 {
		return from, nil
	}

	// find possible duplicates
	for i := range columns {
		for j := i + 1; j < len(columns); j++ {
			if expr.Equivalent(columns[i].Node, columns[j].Node) {
				return nil, fmt.Errorf("duplicate order by expression %q", expr.ToString(columns[j].Node))
			}
		}
	}

	return &OrderBy{
		Nonterminal: Nonterminal{From: from},
		Columns:     columns,
	}, nil
}

func lowerBind(in *pir.Bind, from Op) (Op, error) {
	return &Project{
		Nonterminal: Nonterminal{From: from},
		Using:       in.Bindings(),
	}, nil
}

func (w *walker) lowerUnionMap(in *pir.UnionMap, env Env) (Op, error) {
	sub, err := w.walkBuild(in.Child.Final(), env)
	if err != nil {
		return nil, err
	}
	latest := w.latest
	if latest == -1 {
		// it's possible that the inner node
		// is actually a dummy (we eliminated the iteration);
		// make sure we actually partition the right data!
		w.put(in.Inner)
		latest = w.latest
	}
	sub, err = w.addReplace(sub, in.Child, env)
	if err != nil {
		return nil, err
	}
	// restore this
	w.latest = latest
	if len(in.PartitionBy) > 0 {
		return &UnionPartition{
			Nonterminal: Nonterminal{From: sub},
			By:          in.PartitionBy,
		}, nil
	}
	return &UnionMap{
		Nonterminal: Nonterminal{From: sub},
	}, nil
}

// UploadFS is a blockfmt.UploadFS that can be encoded
// as part of a query plan.
type UploadFS interface {
	blockfmt.UploadFS
	// Encode encodes the UploadFS into the
	// provided buffer.
	Encode(dst *ion.Buffer, st *ion.Symtab) error
}

// UploadEnv is an Env that supports uploading objects
// which enables support for SELECT INTO.
type UploadEnv interface {
	// Uploader returns an UploadFS to use to
	// upload generated objects. This may return
	// nil if the envionment does not support
	// uploading despite implementing the
	// interface.
	Uploader() UploadFS
	// Key returns the key that should be used to
	// sign the index.
	Key() *blockfmt.Key
}

func lowerOutputPart(n *pir.OutputPart, env Env, input Op) (Op, error) {
	if e, ok := env.(UploadEnv); ok {
		if up := e.Uploader(); up != nil {
			op := &OutputPart{
				Basename: n.Basename,
				Store:    up,
			}
			op.From = input
			return op, nil
		}
	}
	return nil, fmt.Errorf("cannot handle INTO with Env that doesn't support UploadEnv")
}

func lowerOutputIndex(n *pir.OutputIndex, env Env, input Op) (Op, error) {
	if e, ok := env.(UploadEnv); ok {
		if up := e.Uploader(); up != nil {
			parts, ok := expr.FlatPath(n.Table)
			if !ok || len(parts) != 2 {
				return nil, fmt.Errorf("invalid table expression %s", expr.ToString(n.Table))
			}
			op := &OutputIndex{
				DB:       parts[0],
				Table:    parts[1],
				Basename: n.Basename,
				Store:    up,
				Key:      e.Key(),
			}
			op.From = input
			return op, nil
		}
	}
	return nil, fmt.Errorf("cannot handle INTO with Env that doesn't support UploadEnv")
}

type input struct {
	table  *expr.Table
	hints  Hints
	handle TableHandle // if already statted
}

func (i *input) finish(env Env) (Input, error) {
	th, err := i.stat(env)
	if err != nil {
		return Input{}, err
	}
	return Input{
		Table:  i.table,
		Handle: th,
	}, nil
}

func (i *input) stat(env Env) (TableHandle, error) {
	if i.handle != nil {
		return i.handle, nil
	}
	th, err := stat(env, i.table.Expr, &i.hints)
	if err != nil {
		return nil, err
	}
	i.handle = th
	return th, nil
}

// conjunctions returns the list of top-level
// conjunctions from a logical expression
// by appending the results to 'lst'
//
// this is used for predicate pushdown so that
//
//	<a> AND <b> AND <c>
//
// can be split and evaluated as early as possible
// in the query-processing pipeline
func conjunctions(e expr.Node, lst []expr.Node) []expr.Node {
	a, ok := e.(*expr.Logical)
	if !ok || a.Op != expr.OpAnd {
		return append(lst, e)
	}
	return conjunctions(a.Left, conjunctions(a.Right, lst))
}

func conjoin(x []expr.Node) expr.Node {
	o := x[0]
	rest := x[1:]
	for _, n := range rest {
		o = expr.And(o, n)
	}
	return o
}

func isTimestamp(e expr.Node) bool {
	_, ok := e.(*expr.Timestamp)
	return ok
}

// canRemoveHint should return true if it
// is "safe" (i.e. likely to be profitable)
// to remove a hint from an input
//
// right now we avoid removing any expressions
// that contain timestamp comparisons
// (or logical compositions thereof)
func canRemoveHint(e expr.Node) bool {
	l, ok := e.(*expr.Logical)
	if ok {
		return canRemoveHint(l.Left) && canRemoveHint(l.Right)
	}
	cmp, ok := e.(*expr.Comparison)
	if !ok {
		return true
	}
	return !(isTimestamp(cmp.Left) || isTimestamp(cmp.Right))
}

func mergeFilterHint(x, y *input) bool {
	var xconj, yconj []expr.Node
	if x.hints.Filter != nil {
		xconj = conjunctions(x.hints.Filter, nil)
	}
	if y.hints.Filter != nil {
		yconj = conjunctions(y.hints.Filter, nil)
	}
	var overlap []expr.Node
	i := 0
outer:
	for ; i < len(xconj) && len(yconj) > 0; i++ {
		v := xconj[i]
		for j := range yconj {
			if expr.Equivalent(yconj[j], v) {
				yconj[j], yconj = yconj[len(yconj)-1], yconj[:len(yconj)-1]
				xconj[i], xconj = xconj[len(xconj)-1], xconj[:len(xconj)-1]
				overlap = append(overlap, v)
				i--
				continue outer
			}
		}
		// not part of an overlap, so
		// make sure we are allowed to
		// eliminate this hint
		if !canRemoveHint(v) {
			return false
		}
	}
	for _, v := range xconj[i:] {
		if !canRemoveHint(v) {
			return false
		}
	}
	// make sure any remaining rhs values
	// can be safely eliminated as well
	for _, v := range yconj {
		if !canRemoveHint(v) {
			return false
		}
	}
	if len(overlap) > 0 {
		x.hints.Filter = conjoin(overlap)
	} else {
		x.hints.Filter = nil
	}
	return true
}

func (i *input) merge(in *input) bool {
	if !i.table.Expr.Equals(in.table.Expr) {
		return false
	}
	if !mergeFilterHint(i, in) {
		return false
	}
	i.handle = nil
	if i.hints.AllFields {
		return true
	}
	if in.hints.AllFields {
		i.hints.Fields = nil
		i.hints.AllFields = true
		return true
	}
	i.hints.Fields = append(i.hints.Fields, in.hints.Fields...)
	slices.Sort(i.hints.Fields)
	i.hints.Fields = slices.Compact(i.hints.Fields)
	return true
}

// A walker is used when walking a pir.Trace to
// accumulate identical inputs so leaf nodes
// that reference the same inputs can be
// deduplicated.
type walker struct {
	inputs []input
	latest int
}

func (w *walker) put(it *pir.IterTable) {
	in := input{
		table: it.Table,
		hints: Hints{
			Filter:    it.Filter,
			Fields:    it.Fields(),
			AllFields: it.Wildcard(),
		},
	}
	for i := range w.inputs {
		if w.inputs[i].merge(&in) {
			w.latest = i
			return
		}
	}
	w.latest = len(w.inputs)
	w.inputs = append(w.inputs, in)
}

func (w *walker) walkBuild(in pir.Step, env Env) (Op, error) {
	// IterTable is the terminal node
	if it, ok := in.(*pir.IterTable); ok {
		var eqparts []expr.Node
		if len(it.OnEqual) > 0 {
			eqparts = make([]expr.Node, len(it.OnEqual))
			for i := range eqparts {
				eqparts[i] = expr.Call(expr.PartitionValue, expr.Integer(i))
			}
		}

		w.put(it) // set w.latest
		out := Op(&Leaf{
			Orig:      it.Table,
			OnEqual:   it.OnEqual,
			EqualExpr: eqparts,
		})

		if it.Filter != nil {
			out = &Filter{
				Nonterminal: Nonterminal{From: out},
				Expr:        it.Filter,
			}
		}
		return out, nil
	}
	// similarly, NoOutput is also terminal
	if _, ok := in.(pir.NoOutput); ok {
		return NoOutput{}, nil
	}
	if _, ok := in.(pir.DummyOutput); ok {
		return DummyOutput{}, nil
	}
	// ... and UnionMap as well
	if u, ok := in.(*pir.UnionMap); ok {
		return w.lowerUnionMap(u, env)
	}

	input, err := w.walkBuild(pir.Input(in), env)
	if err != nil {
		return nil, err
	}
	switch n := in.(type) {
	case *pir.IterValue:
		return lowerIterValue(n, input)
	case *pir.Filter:
		return lowerFilter(n, input)
	case *pir.Distinct:
		return lowerDistinct(n, input)
	case *pir.Bind:
		return lowerBind(n, input)
	case *pir.Aggregate:
		return lowerAggregate(n, input)
	case *pir.Limit:
		return lowerLimit(n, input)
	case *pir.Order:
		return lowerOrder(n, input)
	case *pir.OutputIndex:
		return lowerOutputIndex(n, env, input)
	case *pir.OutputPart:
		return lowerOutputPart(n, env, input)
	case *pir.Unpivot:
		return lowerUnpivot(n, input)
	case *pir.UnpivotAtDistinct:
		return lowerUnpivotAtDistinct(n, input)
	default:
		return nil, fmt.Errorf("don't know how to lower %T", in)
	}
}

func (w *walker) finish(env Env) ([]Input, error) {
	if w.inputs == nil {
		return nil, nil
	}
	inputs := make([]Input, len(w.inputs))
	for i := range w.inputs {
		in, err := w.inputs[i].finish(env)
		if err != nil {
			return nil, err
		}
		inputs[i] = in
	}
	return inputs, nil
}

// Result is a (field, type) tuple
// that indicates the possible output encoding
// of a particular field
type Result struct {
	Name string
	Type expr.TypeSet
}

// ResultSet is an ordered list of Results
type ResultSet []Result

func results(b *pir.Trace) ResultSet {
	final := b.FinalBindings()
	if len(final) == 0 {
		return nil
	}
	types := b.FinalTypes()
	out := make(ResultSet, len(final))
	for i := range final {
		out[i] = Result{Name: final[i].Result(), Type: types[i]}
	}
	return out
}

func toTree(in *pir.Trace, env Env) (*Tree, error) {
	w := walker{latest: -1}
	t := &Tree{}
	err := w.toNode(&t.Root, in, env)
	if err != nil {
		return nil, err
	}
	t.Inputs, err = w.finish(env)
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (w *walker) addReplace(op Op, in *pir.Trace, env Env) (Op, error) {
	if len(in.Replacements) == 0 {
		return op, nil
	}
	// push a substitution node for replacements if necessary
	inner := make([]*Node, len(in.Replacements))
	for i := range in.Replacements {
		inner[i] = &Node{}
		err := w.toNode(inner[i], in.Replacements[i], env)
		if err != nil {
			return nil, err
		}
	}
	return &Substitute{
		Nonterminal: Nonterminal{op},
		Inner:       inner,
	}, nil
}

func (w *walker) toNode(t *Node, in *pir.Trace, env Env) error {
	w.latest = -1
	op, err := w.walkBuild(in.Final(), env)
	if err != nil {
		return err
	}
	t.Input = w.latest
	op, err = w.addReplace(op, in, env)
	if err != nil {
		return err
	}
	t.Op = op
	t.OutputType = results(in)
	return nil
}

type pirenv struct {
	env Env
}

func (e pirenv) Schema(tbl expr.Node) expr.Hint {
	s, ok := e.env.(Schemer)
	if !ok {
		return nil
	}
	return s.Schema(tbl)
}

func (e pirenv) Index(tbl expr.Node) (pir.Index, error) {
	idx, ok := e.env.(Indexer)
	if !ok {
		return nil, nil
	}
	return index(idx, tbl)
}

// New creates a new Tree from raw query AST.
func New(q *expr.Query, env Env) (*Tree, error) {
	return newTree(q, env, false)
}

// NewSplit creates a new Tree from raw query AST.
func NewSplit(q *expr.Query, env Env) (*Tree, error) {
	return newTree(q, env, true)
}

func newTree(q *expr.Query, env Env, split bool) (*Tree, error) {
	b, err := pir.Build(q, pirenv{env})
	if err != nil {
		return nil, err
	}
	if split {
		reduce, err := pir.Split(b)
		if err != nil {
			return nil, err
		}
		b = reduce
	} else {
		b = pir.NoSplit(b)
	}

	tree, err := toTree(b, env)
	if err != nil {
		return nil, err
	}

	if q.Explain == expr.ExplainNone {
		return tree, nil
	}

	// explain the query
	op := &Explain{
		Format: q.Explain,
		Query:  q,
		Tree:   tree,
	}

	res := &Tree{Inputs: tree.Inputs, Root: Node{Op: op}}
	return res, nil

}

func lowerUnpivot(in *pir.Unpivot, from Op) (Op, error) {
	u := &Unpivot{
		Nonterminal: Nonterminal{From: from},
		As:          in.Ast.As,
		At:          in.Ast.At,
	}
	return u, nil
}

func lowerUnpivotAtDistinct(in *pir.UnpivotAtDistinct, from Op) (Op, error) {
	u := &UnpivotAtDistinct{
		Nonterminal: Nonterminal{From: from},
		At:          *in.Ast.At,
	}
	return u, nil
}
