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

package expr

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"

	"github.com/SnellerInc/sneller/date"
	"github.com/SnellerInc/sneller/internal/stringext"
	"github.com/SnellerInc/sneller/ion"

	"golang.org/x/exp/slices"
)

// Visitor is an interface that must
// be satisfied by the argument to Visit.
//
// A Visitor's Visit method is invoked for each node encountered by Walk. If
// the result visitor w is not nil, Walk visits each of the children of node
// with the visitor w, followed by a call of w.Visit(nil).
//
// (see also: ast.Visitor)
type Visitor interface {
	Visit(Node) Visitor
}

// Rewriter accepts a Node and returns
// a new node (or just its argument)
type Rewriter interface {
	// Rewrite is applied to nodes
	// in depth-first order, and each
	// node is re-written to use the
	// returned value.
	Rewrite(Node) Node

	// Walk is called during node traversal
	// and the returned Rewriter is used for
	// all the children of Node.
	// If the returned rewriter is nil,
	// then traversal does not proceed past Node.
	Walk(Node) Rewriter
}

type nonleaf interface {
	rewrite(r Rewriter) Node
}

// Rewrite recursively applies a Rewriter in depth-first order
func Rewrite(r Rewriter, n Node) Node {
	if n == nil {
		return nil
	}
	nl, ok := n.(nonleaf)
	if ok {
		rc := r.Walk(n)
		if rc != nil {
			n = nl.rewrite(rc)
		}
	}
	n = r.Rewrite(n)
	return n
}

// Walk traverses an AST in depth-first order: It starts by calling
// v.Visit(node); node must not be nil. If the visitor w returned by
// v.Visit(node) is not nil, Walk is invoked recursively with visitor w for
// each of the non-nil children of node, followed by a call of w.Visit(nil).
//
// (see also: ast.Walk)
func Walk(v Visitor, n Node) {
	w := v.Visit(n)
	if w != nil {
		n.walk(w)
		w.Visit(nil)
	}
}

type WalkFunc func(Node) bool

func (w WalkFunc) Visit(e Node) Visitor {
	if w(e) {
		return w
	}

	return nil
}

// AggregateOp is one of the aggregation operations
type AggregateOp int

const (
	// Invalid or no aggregate operation - if you see this it means that the value was either not
	// initialized yet or it describes non-aggregate operation (for example ssainfo.aggregateOp).
	OpNone AggregateOp = iota

	// Describes SQL COUNT(...) aggregate operation
	OpCount

	// Describes SQL SUM(...) aggregate operation.
	OpSum

	// Describes SQL AVG(...) aggregate
	OpAvg

	// Describes SQL MIN(...) aggregate operation.
	OpMin

	// Describes SQL MAX(...) aggregate operation.
	OpMax

	// Describes SQL COUNT(DISTINCT ...) operation
	OpCountDistinct

	// OpSumInt is equivalent to the SUM() operation,
	// except that it only accepts integer inputs
	// (and therefore always produces an integer output)
	OpSumInt

	// OpSumCount is equivalent to the SUM() operation,
	// except that it evaluates to 0 instead of
	// NULL if there are no inputs. This should be
	// used to aggregate COUNT(...) results instead
	// of SUM_INT.
	OpSumCount

	// Describes SQL BIT_AND(...) aggregate operation.
	OpBitAnd

	// Describes SQL BIT_OR(...) aggregate operation.
	OpBitOr

	// Describes SQL BIT_XOR(...) aggregate operation.
	OpBitXor

	// Describes SQL BOOL_AND(...) aggregate operation.
	OpBoolAnd

	// Describes SQL BOOL_OR(...) aggregate operation.
	OpBoolOr

	// Describes SQL MIN(timestamp).
	//
	// EARLIEST() function is used by Sneller to distinguish
	// between arithmetic vs timestamp aggregation
	OpEarliest

	// Describes SQL MAX(timestamp).
	//
	// LATEST() function is used by Sneller to distinguish
	// between arithmetic vs timestamp aggregation
	OpLatest

	// Describes APPROX_COUNT_DISTINCT aggregate, that produces
	// an integer output
	OpApproxCountDistinct

	// Describes APPROX_COUNT_DISTINCT aggregate run on a single node,
	// that produces some intermediate data.
	OpApproxCountDistinctPartial

	// Describes APPROX_COUNT_DISTINCT aggregate that merges
	// intermediate data and yields the final integer value
	OpApproxCountDistinctMerge

	// OpVariancePop is equivalent to the VARIANCE() and VARIANCE_POP()
	// operation and calculates the population variance
	OpVariancePop

	// OpStdDevPop is equivalent to the STDDEV() and STDDEV_POP() operation
	// Note that it does not calculate the sample standard deviation
	OpStdDevPop

	// OpRowNumber corresponds to ROW_NUMBER()
	OpRowNumber

	// OpRank corresponds to RANK()
	OpRank

	// OpDenseRank corresponds to DENSE_RANK()
	OpDenseRank

	// Describes SNELLER_DATASHAPE aggregate
	OpSystemDatashape

	// Describes SNELLER_DATASHAPE_MERGE aggregate, that
	// merges the results from multiple SNELLER_DATASHAPE
	// aggregates.
	OpSystemDatashapeMerge

	maxAggregateOp
)

const (
	ApproxCountDistinctMinPrecision     = 4
	ApproxCountDistinctMaxPrecision     = 16
	ApproxCountDistinctDefaultPrecision = 11
)

func (a AggregateOp) defaultResult() string {
	switch a {
	case OpCount, OpCountDistinct, OpSumCount, OpApproxCountDistinct:
		return "count"
	case OpSum, OpSumInt:
		return "sum"
	case OpAvg:
		return "avg"
	case OpVariancePop:
		return "variance_pop"
	case OpStdDevPop:
		return "stddev_pop"
	case OpMin, OpEarliest:
		return "min"
	case OpMax, OpLatest:
		return "max"
	case OpSystemDatashape:
		return "datashape"
	case OpRowNumber:
		return "row_number"
	case OpRank:
		return "rank"
	case OpDenseRank:
		return "dense_rank"
	default:
		return ""
	}
}

func (a AggregateOp) String() string {
	switch a {
	case OpCount:
		return "COUNT"
	case OpSum:
		return "SUM"
	case OpAvg:
		return "AVG"
	case OpVariancePop:
		return "VARIANCE_POP"
	case OpStdDevPop:
		return "STDDEV_POP"
	case OpMin:
		return "MIN"
	case OpMax:
		return "MAX"
	case OpCountDistinct:
		return "COUNT DISTINCT"
	case OpSumInt:
		return "SUM_INT"
	case OpSumCount:
		return "SUM_COUNT"
	case OpEarliest:
		return "EARLIEST"
	case OpLatest:
		return "LATEST"
	case OpBitAnd:
		return "BIT_AND"
	case OpBitOr:
		return "BIT_OR"
	case OpBitXor:
		return "BIT_XOR"
	case OpBoolAnd:
		return "BOOL_AND"
	case OpBoolOr:
		return "BOOL_OR"
	case OpApproxCountDistinct:
		return "APPROX_COUNT_DISTINCT"
	case OpApproxCountDistinctPartial:
		return "APPROX_COUNT_DISTINCT_PARTIAL"
	case OpApproxCountDistinctMerge:
		return "APPROX_COUNT_DISTINCT_MERGE"
	case OpRowNumber:
		return "ROW_NUMBER"
	case OpRank:
		return "RANK"
	case OpDenseRank:
		return "DENSE_RANK"
	case OpSystemDatashape:
		return "SNELLER_DATASHAPE"
	case OpSystemDatashapeMerge:
		return "SNELLER_DATASHAPE_MERGE"
	default:
		return fmt.Sprintf("<AggregateOp=%d>", int(a))
	}
}

func (a AggregateOp) private() bool {
	switch a {
	case OpCount, OpSum, OpAvg, OpVariancePop, OpStdDevPop, OpMin, OpMax, OpEarliest, OpLatest,
		OpBitAnd, OpBitOr, OpBitXor, OpBoolAnd, OpBoolOr,
		OpApproxCountDistinct, OpSystemDatashape, OpRowNumber, OpRank, OpDenseRank:
		return false
	}

	return true
}

// WindowOnly returns whether or no the aggregate op
// is only valid when used with a window function
func (a AggregateOp) WindowOnly() bool {
	switch a {
	case OpRowNumber, OpRank, OpDenseRank:
		return true
	default:
		return false
	}
}

// AcceptDistinct returns true if the aggregate can be used with DISTINCT keyword.
func (a AggregateOp) AcceptDistinct() bool {
	switch a {
	case OpCount, OpMin, OpMax, OpCountDistinct, OpEarliest, OpLatest:
		return true
	}

	return false
}

// AcceptStar returns true if the aggregate can be used with '*'.
func (a AggregateOp) AcceptStar() bool {
	switch a {
	case OpCount, OpSystemDatashape:
		return true
	}

	return false
}

// AcceptExpression returns true if the aggregate can be used with an arbitrary expression.
func (a AggregateOp) AcceptExpression() bool {
	return a != OpSystemDatashape
}

// Aggregate is an aggregation expression
type Aggregate struct {
	// Op is the aggregation operation
	// (sum, min, max, etc.)
	Op AggregateOp
	// Precision is the parameter for OpApproxCountDistinct
	Precision uint8
	// Inner is the expression to be aggregated;
	// this may be nil when the operation is a window function
	Inner Node
	// Over, if non-nil, is the OVER part
	// of the aggregation
	Over *Window
	// Filter is an optional filtering expression
	Filter Node
}

func (a *Aggregate) Equals(e Node) bool {
	ea, ok := e.(*Aggregate)
	if !ok {
		return false
	}
	if ea.Op != a.Op || (a.Inner == nil) != (ea.Inner == nil) ||
		(a.Inner != nil && !a.Inner.Equals(ea.Inner)) {
		return false
	}
	if ea.Precision != a.Precision {
		return false
	}

	if (a.Filter != nil) != (ea.Filter != nil) {
		return false
	}
	if (a.Filter != nil) && !a.Filter.Equals(ea.Filter) {
		return false
	}

	if a.Over == nil {
		return ea.Over == nil
	}
	if !slices.EqualFunc(a.Over.PartitionBy, ea.Over.PartitionBy, Equivalent) {
		return false
	}
	return slices.EqualFunc(a.Over.OrderBy, ea.Over.OrderBy, Order.Equals)
}

func settype(dst *ion.Buffer, st *ion.Symtab, str string) {
	dst.BeginField(st.Intern("type"))
	dst.WriteSymbol(st.Intern(str))
}

func (a *Aggregate) Encode(dst *ion.Buffer, st *ion.Symtab) {
	dst.BeginStruct(-1)
	settype(dst, st, "aggregate")
	dst.BeginField(st.Intern("op"))
	dst.WriteUint(uint64(a.Op))
	if a.Op == OpApproxCountDistinct || a.Op == OpApproxCountDistinctPartial || a.Op == OpApproxCountDistinctMerge {
		dst.BeginField(st.Intern("precision"))
		dst.WriteUint(uint64(a.Precision))
	}
	if a.Inner != nil {
		dst.BeginField(st.Intern("inner"))
		a.Inner.Encode(dst, st)
	}

	if a.Over != nil {
		dst.BeginField(st.Intern("over_partition"))
		dst.BeginList(-1)
		for i := range a.Over.PartitionBy {
			a.Over.PartitionBy[i].Encode(dst, st)
		}
		dst.EndList()
		if len(a.Over.OrderBy) > 0 {
			dst.BeginField(st.Intern("over_order_by"))
			EncodeOrder(a.Over.OrderBy, dst, st)
		}
	}

	if a.Filter != nil {
		dst.BeginField(st.Intern("filter_where"))
		a.Filter.Encode(dst, st)
	}

	dst.EndStruct()
}

func (a *Aggregate) SetField(f ion.Field) error {
	switch f.Label {
	case "op":
		u, err := f.Uint()
		if err != nil {
			return err
		}
		a.Op = AggregateOp(u)
	case "inner":
		var err error
		a.Inner, err = Decode(f.Datum)
		return err
	case "over_partition":
		if a.Over == nil {
			a.Over = new(Window)
		}
		return f.UnpackList(func(d ion.Datum) error {
			item, err := Decode(d)
			if err != nil {
				return err
			}
			a.Over.PartitionBy = append(a.Over.PartitionBy, item)
			return nil
		})
	case "over_order_by":
		if a.Over == nil {
			a.Over = new(Window)
		}
		var err error
		a.Over.OrderBy, err = decodeOrder(f.Datum)
		return err
	case "filter_where":
		var err error
		a.Filter, err = Decode(f.Datum)
		return err
	case "precision":
		p, err := f.Uint()
		if err != nil {
			return err
		}
		a.Precision = uint8(p)
	default:
		return errUnexpectedField
	}
	return nil
}

func (a *Aggregate) text(dst *strings.Builder, redact bool) {
	switch a.Op {
	case OpCountDistinct:
		dst.WriteString("COUNT(DISTINCT ")
		a.Inner.text(dst, redact)
		dst.WriteByte(')')

	case OpApproxCountDistinct, OpApproxCountDistinctPartial, OpApproxCountDistinctMerge:
		dst.WriteString(a.Op.String())
		dst.WriteByte('(')
		a.Inner.text(dst, redact)
		if a.Precision > 0 && a.Precision != ApproxCountDistinctDefaultPrecision {
			fmt.Fprintf(dst, ", %d", a.Precision)
		}
		dst.WriteByte(')')

	default:
		dst.WriteString(a.Op.String())
		dst.WriteByte('(')
		if a.Inner != nil {
			a.Inner.text(dst, redact)
		}
		dst.WriteByte(')')
	}

	if a.Filter != nil {
		dst.WriteString(" FILTER (WHERE ")
		a.Filter.text(dst, redact)
		dst.WriteString(")")
	}

	if a.Over != nil {
		dst.WriteString(" OVER (")
		for i := range a.Over.PartitionBy {
			if i == 0 {
				dst.WriteString("PARTITION BY ")
			} else {
				dst.WriteString(", ")
			}
			a.Over.PartitionBy[i].text(dst, redact)
		}
		for i := range a.Over.OrderBy {
			if i == 0 {
				if len(a.Over.PartitionBy) > 0 {
					dst.WriteByte(' ')
				}
				dst.WriteString("ORDER BY ")
			} else {
				dst.WriteString(", ")
			}
			a.Over.OrderBy[i].text(dst, redact)
		}
		dst.WriteByte(')')
	}
}

func (a *Aggregate) walk(v Visitor) {
	if a.Inner != nil {
		Walk(v, a.Inner)
	}
	if a.Over != nil {
		for i := range a.Over.PartitionBy {
			Walk(v, a.Over.PartitionBy[i])
		}
		for i := range a.Over.OrderBy {
			Walk(v, a.Over.OrderBy[i].Column)
		}
	}
	if a.Filter != nil {
		Walk(v, a.Filter)
	}
}

func (a *Aggregate) rewrite(r Rewriter) Node {
	if a.Inner != nil {
		a.Inner = Rewrite(r, a.Inner)
	}
	if a.Over != nil {
		for i := range a.Over.PartitionBy {
			a.Over.PartitionBy[i] = Rewrite(r, a.Over.PartitionBy[i])
		}
		for i := range a.Over.OrderBy {
			a.Over.OrderBy[i].Column = Rewrite(r, a.Over.OrderBy[i].Column)
		}
	}
	if a.Filter != nil {
		a.Filter = Rewrite(r, a.Filter)
	}
	return a
}

func (a *Aggregate) typeof(h Hint) TypeSet {
	switch a.Op {
	case OpCount, OpCountDistinct, OpSumCount, OpApproxCountDistinct, OpRowNumber, OpRank, OpDenseRank:
		return UnsignedType
	case OpSumInt:
		// if the inner type is only ever unsigned,
		// then the result is only ever unsigned,
		// etc.
		return TypeOf(a.Inner, h)
	case OpLatest, OpEarliest:
		return TimeType | NullType
	case OpSystemDatashape:
		return StructType
	default:
		return NumericType | NullType
	}
}

// IsDistinct returns if the aggregate has DISTINCT clause.
func (a *Aggregate) IsDistinct() bool {
	return a.Op == OpCountDistinct
}

// Count produces the COUNT(e) aggregate
func Count(e Node) *Aggregate { return &Aggregate{Op: OpCount, Inner: e} }

// CountNonNull counts the number of non-null rows
func CountNonNull(e Node) *Aggregate {
	// Consider creating ASM aggregator comparable to the row counter in average aggregator
	return SumInt(IfThenElse(Is(e, IsNotNull), Integer(1), Integer(0)))
}

// CountDistinct produces the COUNT(DISTINCT e) aggregate
func CountDistinct(e Node) *Aggregate { return &Aggregate{Op: OpCountDistinct, Inner: e} }

// Sum produces the SUM(e) aggregate
func Sum(e Node) *Aggregate { return &Aggregate{Op: OpSum, Inner: e} }

// SumInt produces the SUM(e) aggregate
// that is guaranteed to operate only on
// integer inputs
func SumInt(e Node) *Aggregate { return &Aggregate{Op: OpSumInt, Inner: e} }

// SumCount produces the SUM(e) aggregate
// that may be used to aggregate
// COUNT(...) results
func SumCount(e Node) *Aggregate { return &Aggregate{Op: OpSumCount, Inner: e} }

// Avg produces the AVG(e) aggregate
func Avg(e Node) *Aggregate { return &Aggregate{Op: OpAvg, Inner: e} }

// Min produces the MIN(e) aggregate
func Min(e Node) *Aggregate { return &Aggregate{Op: OpMin, Inner: e} }

// Max produces the MAX(e) aggregate
func Max(e Node) *Aggregate { return &Aggregate{Op: OpMax, Inner: e} }

func AggregateAnd(e Node) *Aggregate { return &Aggregate{Op: OpBitAnd, Inner: e} }
func AggregateOr(e Node) *Aggregate  { return &Aggregate{Op: OpBitOr, Inner: e} }
func AggregateXor(e Node) *Aggregate { return &Aggregate{Op: OpBitXor, Inner: e} }

func AggregateBoolAnd(e Node) *Aggregate { return &Aggregate{Op: OpBoolAnd, Inner: e} }
func AggregateBoolOr(e Node) *Aggregate  { return &Aggregate{Op: OpBoolOr, Inner: e} }

// Earliest produces the EARLIEST(timestamp) aggregate
func Earliest(e Node) *Aggregate { return &Aggregate{Op: OpEarliest, Inner: e} }

// Latest produces the LATEST(timestamp) aggregate
func Latest(e Node) *Aggregate { return &Aggregate{Op: OpLatest, Inner: e} }

// Equivalent returns whether two nodes
// are equivalent.
//
// Two nodes are equal if they are
// equivalent numbers (i.e. '0' and '0.0')
// or if they are identical.
func Equivalent(a, b Node) bool {
	if a == b {
		return true
	}
	return a.Equals(b)
}

// IsIdentifier returns whether e == Ident(s),
// in other words, a path expression with a
// single component that is equivalent to the
// given string.
func IsIdentifier(e Node, s string) bool {
	return e == Ident(s)
}

// IsPath returns whether or not e is a path expression.
// A path expression is composed entirely of Ident, Dot, and Index operations.
func IsPath(e Node) bool {
	switch t := e.(type) {
	case Ident:
		return true
	case *Dot:
		return IsPath(t.Inner)
	case *Index:
		return IsPath(t.Inner)
	default:
		return false
	}
}

// MakePath constructs a path expression
// from a list of identifier path components.
// This is the reverse operation of FlatPath.
func MakePath(path []string) Node {
	p := Node(Ident(path[0]))
	for _, field := range path[1:] {
		p = &Dot{Inner: p, Field: field}
	}
	return p
}

// FlatPath attempts to flatten e into
// a list of path components. For example,
// the expression a.b.c would expand to
//
//	[]string{"a", "b", "c"}
//
// FlatPath returns (nil, false) if e is
// not a path or the path cannot be flattened.
func FlatPath(e Node) ([]string, bool) {
	switch e := e.(type) {
	case Ident:
		return []string{string(e)}, true
	case *Dot:
		fields, ok := FlatPath(e.Inner)
		if ok {
			return append(fields, e.Field), true
		}
		return nil, false
	default:
		return nil, false
	}
}

// Window is a window function call
type Window struct {
	PartitionBy []Node
	OrderBy     []Order
}

// ToString returns the string
// representation of this AST node
// and its children in approximately
// PartiQL syntax
func ToString(p Printable) string {
	if p == nil {
		return "<nil>"
	}
	var dst strings.Builder
	p.text(&dst, false)
	return dst.String()
}

// ToRedacted returns the string
// representation of this AST node
// and its children in approximately PartiQL syntax,
// but with all constant expressions replaced
// with random (deterministic) values.
func ToRedacted(p Printable) string {
	if p == nil {
		return "<nil>"
	}
	var dst strings.Builder
	p.text(&dst, true)
	return dst.String()
}

type Printable interface {
	// text should write the textual representation
	// of this node to dst, and should redact itself
	// if it is a constant and redact is true
	text(dst *strings.Builder, redact bool)
}

// Node is an expression AST node
type Node interface {
	Printable
	// Equals returns whether this node
	// is equivalent to another node.
	// Nodes are Equal if they are
	// syntactically equivalent or correspond
	// to equal numeric values.
	Equals(Node) bool

	Encode(dst *ion.Buffer, st *ion.Symtab)

	walk(Visitor)
}

// Equal returns whether a and b are equivalent.
// a or b may be nil.
func Equal(a, b Node) bool {
	if a == nil {
		return b == nil
	}
	return b != nil && a.Equals(b)
}

// Constant is a Node that is
// a constant value.
type Constant interface {
	Node
	// Datum returns the ion Datum
	// associated with this constant.
	Datum() ion.Datum
}

var (
	// these are all the Constant types
	_ Constant = String("")
	_ Constant = Integer(0)
	_ Constant = Float(0)
	_ Constant = Bool(true)
	_ Constant = (*Timestamp)(nil)
	_ Constant = Null{}
	_ Constant = (*Rational)(nil)
)

// IsConstant returns true if node is a constant value
func IsConstant(e Node) bool {
	_, ok := e.(Constant)
	return ok
}

type stronglyTyped interface {
	Type() TypeSet
}

type weaklyTyped interface {
	typeof(rw Hint) TypeSet
}

// TypeOf attempts to return the set
// of types that a node could evaluate
// to at runtime.
func TypeOf(n Node, h Hint) TypeSet {
	if h == nil {
		h = NoHint
	}
	// identifiers can only be typed via hints by definition
	if _, ok := n.(Ident); ok {
		return h.TypeOf(n)
	}
	if st, ok := n.(stronglyTyped); ok {
		return st.Type()
	}
	if tn, ok := n.(weaklyTyped); ok {
		return tn.typeof(h)
	}
	return AnyType
}

type Bool bool

func (b Bool) text(dst *strings.Builder, redact bool) {
	if b {
		dst.WriteString("TRUE")
	} else {
		dst.WriteString("FALSE")
	}
}

func (b Bool) Encode(dst *ion.Buffer, st *ion.Symtab) {
	dst.WriteBool(bool(b))
}

func (b Bool) Equals(e Node) bool {
	eb, ok := e.(Bool)
	if !ok {
		return false
	}
	return b == eb
}

func (b Bool) walk(v Visitor) {}

func (b Bool) invert() Node {
	return !b
}

func (b Bool) Type() TypeSet {
	return oneType(ion.BoolType)
}

func (b Bool) Datum() ion.Datum {
	return ion.Bool(bool(b))
}

// String is a literal string AST node
type String string

func (s String) text(dst *strings.Builder, redact bool) {
	v := string(s)
	if redact {
		v = redactString(v)
	}
	quote(dst, v)
}

func (s String) Datum() ion.Datum {
	return ion.String(string(s))
}

func (s String) walk(v Visitor) {}

func (s String) Type() TypeSet {
	return oneType(ion.StringType)
}

func (s String) Equals(e Node) bool {
	es, ok := e.(String)
	if !ok {
		return false
	}
	return s == es
}

func (s String) Encode(dst *ion.Buffer, _ *ion.Symtab) {
	dst.WriteString(string(s))
}

// Float is a literal float AST node
type Float float64

func (f Float) text(dst *strings.Builder, redact bool) {
	var buf [32]byte
	v := float64(f)
	if redact {
		v = redactFloat(v)
	}
	dst.Write(strconv.AppendFloat(buf[:0], v, 'g', -1, 64))
}

func (f Float) rat() *big.Rat {
	// Note: big.Rat cannot carry infinity nor NaN - for such
	//       float64 values the method would return nil.
	return new(big.Rat).SetFloat64(float64(f))
}

func (f Float) walk(v Visitor) {}

func (f Float) Type() TypeSet {
	return NumericType
}

func (f Float) Encode(dst *ion.Buffer, st *ion.Symtab) {
	dst.WriteFloat64(float64(f))
}

func (f Float) Datum() ion.Datum {
	return ion.Float(float64(f))
}

func (f Float) Equals(e Node) bool {
	ef, ok := e.(Float)
	if ok {
		return f == ef
	}
	ei, ok := e.(Integer)
	if ok {
		return float64(f) == float64(int64(ei))
	}
	ri, ok := e.(*Rational)
	if ok {
		rif, ok := (*big.Rat)(ri).Float64()
		return ok && rif == float64(f)
	}
	return false
}

func (f Float) isint() bool {
	return Float(int64(f)) == f
}

var NaN = Float(math.NaN())

// Integer is a literal integer AST node
type Integer int64

func (i Integer) text(dst *strings.Builder, redact bool) {
	var buf [32]byte
	v := int64(i)
	if redact {
		v = redactInt(v)
	}
	dst.Write(strconv.AppendInt(buf[:0], v, 10))
}

func (i Integer) rat() *big.Rat {
	return new(big.Rat).SetInt64(int64(i))
}

func (i Integer) walk(v Visitor) {}

func (i Integer) Type() TypeSet {
	return types(ion.UintType, ion.IntType)
}

func (i Integer) Datum() ion.Datum {
	return ion.Int(int64(i))
}

func (i Integer) Encode(dst *ion.Buffer, _ *ion.Symtab) {
	dst.WriteInt(int64(i))
}

func (i Integer) Equals(e Node) bool {
	ei, ok := e.(Integer)
	if ok {
		return ei == i
	}
	ef, ok := e.(Float)
	if ok {
		trunc := int64(ef)
		return float64(trunc) == float64(ef) && trunc == int64(i)
	}
	er, ok := e.(*Rational)
	if ok && (*big.Rat)(er).IsInt() {
		num := (*big.Rat)(er).Num()
		if num.IsInt64() {
			return num.Int64() == int64(i)
		}
	}
	return false
}

type Rational big.Rat

func (r *Rational) text(dst *strings.Builder, redact bool) {
	br := (*big.Rat)(r)
	if redact {
		// just map this onto the [0, 1) interval
		// like we do for floats
		f, _ := br.Float64()
		Float(f).text(dst, redact)
		return
	}

	// if this is an integer, just print that
	if br.IsInt() {
		dst.WriteString(br.Num().String())
		return
	}
	// format as fp if we have full precision
	f, ok := br.Float64()
	if ok {
		dst.WriteString(strconv.FormatFloat(f, 'f', -1, 64))
		return
	}
	dst.WriteString(br.String())
}

func (r *Rational) walk(v Visitor) {}

func (r *Rational) rat() *big.Rat { return (*big.Rat)(r) }

func (r *Rational) Type() TypeSet {
	return NumericType
}

func (r *Rational) Encode(dst *ion.Buffer, st *ion.Symtab) {
	dst.BeginStruct(-1)
	settype(dst, st, "rat")
	dst.BeginField(st.Intern("blob"))
	// n.b. this encoding just packs together
	// the numerator and denominator; see
	// math/big/ratmarsh.go
	text, _ := (*big.Rat)(r).GobEncode()
	dst.WriteBlob(text)
	dst.EndStruct()
}

func (r *Rational) SetField(f ion.Field) error {
	switch f.Label {
	case "blob":
		mem, err := f.BlobShared()
		if err != nil {
			return err
		}
		err = (*big.Rat)(r).GobDecode(mem)
		if err != nil {
			return fmt.Errorf("invalid rational blob: %w", err)
		}
	default:
		return errUnexpectedField
	}

	return nil
}

func sign(f float64) int {
	if math.Signbit(f) {
		return -1
	}
	return 1
}

func (r *Rational) Equals(e Node) bool {
	er, ok := e.(*Rational)
	if ok {
		return (*big.Rat)(er).Cmp((*big.Rat)(r)) == 0
	}
	ef, ok := e.(Float)
	if ok {
		f, ok := (*big.Rat)(r).Float64()
		if !ok && math.IsInf(f, 0) && math.IsInf(float64(ef), sign(f)) {
			return true
		}
		return math.Abs(f-float64(ef)) < 1e-16
	}
	if (*big.Rat)(r).IsInt() {
		ei, ok := e.(Integer)
		if ok {
			num := (*big.Rat)(r).Num()
			if num.IsInt64() {
				return int64(ei) == num.Int64()
			}
		}
	}
	return false
}

func (r *Rational) Datum() ion.Datum {
	br := (*big.Rat)(r)
	if br.IsInt() {
		num := br.Num()
		if num.IsInt64() {
			return ion.Int(num.Int64())
		}
	}
	f, _ := br.Float64()
	return ion.Float(f)
}

// Ident is a top-level identifier
type Ident string

func (i Ident) text(dst *strings.Builder, redact bool) {
	dst.WriteString(QuoteID(string(i)))
}

func (i Ident) Encode(dst *ion.Buffer, st *ion.Symtab) {
	dst.WriteSymbol(st.Intern(string(i)))
}

func (i Ident) walk(v Visitor) {}

func (i Ident) Equals(x Node) bool {
	i2, ok := x.(Ident)
	return ok && i == i2
}

// Dot represents the '.' infix operator, i.e.
//
//	Inner '.' Field
//
// The Inner value within Dot should be structure-typed.
type Dot struct {
	Inner Node
	Field string
}

func (d *Dot) text(dst *strings.Builder, redact bool) {
	d.Inner.text(dst, redact)
	dst.WriteByte('.')
	dst.WriteString(QuoteID(d.Field))
}

func (d *Dot) Encode(dst *ion.Buffer, st *ion.Symtab) {
	dst.BeginStruct(-1)
	settype(dst, st, "dot")
	dst.BeginField(st.Intern("inner"))
	d.Inner.Encode(dst, st)
	dst.BeginField(st.Intern("field"))
	dst.WriteSymbol(st.Intern(d.Field))
	dst.EndStruct()
}

func (d *Dot) SetField(f ion.Field) (err error) {
	switch f.Label {
	case "inner":
		d.Inner, err = Decode(f.Datum)
	case "field":
		d.Field, err = f.String()
	default:
		return errUnexpectedField
	}
	return err
}

func (d *Dot) Equals(x Node) bool {
	d2, ok := x.(*Dot)
	return ok && d2.Field == d.Field &&
		d.Inner.Equals(d2.Inner)
}

func (d *Dot) walk(v Visitor) {
	Walk(v, d.Inner)
}

func (d *Dot) rewrite(r Rewriter) Node {
	d.Inner = Rewrite(r, d.Inner)
	return d
}

// {'x': v}.x -> v
func (d *Dot) simplify(h Hint) Node {
	if b, ok := d.Inner.(*Builtin); ok && b.Func == MakeStruct {
		for i := 0; i < len(b.Args); i += 2 {
			str := b.Args[i].(String)
			if string(str) == d.Field {
				return b.Args[i+1]
			}
		}
		return Missing{}
	}
	if s, ok := d.Inner.(*Struct); ok {
		for i := range s.Fields {
			if s.Fields[i].Label == d.Field {
				return s.Fields[i].Value
			}
		}
		return Missing{}
	}
	return d
}

// Index represents the '[]' infix operator, i.e.
//
//	Inner '[' Offset ']'
//
// The Inner value within Index should be list-typed.
type Index struct {
	Inner  Node
	Offset int // offset is constant for now
}

func (i *Index) text(dst *strings.Builder, redact bool) {
	i.Inner.text(dst, redact)
	fmt.Fprintf(dst, "[%d]", i.Offset)
}

func (i *Index) Encode(dst *ion.Buffer, st *ion.Symtab) {
	dst.BeginStruct(-1)
	settype(dst, st, "index")
	dst.BeginField(st.Intern("inner"))
	i.Inner.Encode(dst, st)
	dst.BeginField(st.Intern("offset"))
	dst.WriteInt(int64(i.Offset))
	dst.EndStruct()
}

func (i *Index) SetField(f ion.Field) (err error) {
	switch f.Label {
	case "inner":
		i.Inner, err = Decode(f.Datum)
	case "offset":
		var v int64
		v, err = f.Int()
		if err == nil {
			i.Offset = int(v)
		}
	default:
		return errUnexpectedField
	}
	return err
}

// [ v ][0] -> v
func (i *Index) simplify(h Hint) Node {
	if b, ok := i.Inner.(*Builtin); ok && b.Func == MakeList {
		if i.Offset < len(b.Args) {
			return b.Args[i.Offset]
		}
		return Missing{}
	}
	if l, ok := i.Inner.(*List); ok {
		if i.Offset < len(l.Values) {
			return l.Values[i.Offset]
		}
		return Missing{}
	}
	return i
}

func (i *Index) Equals(x Node) bool {
	i2, ok := x.(*Index)
	return ok && i.Offset == i2.Offset &&
		i.Inner.Equals(i2.Inner)
}

func (i *Index) walk(v Visitor) {
	Walk(v, i.Inner)
}

func (i *Index) rewrite(r Rewriter) Node {
	i.Inner = Rewrite(r, i.Inner)
	return i
}

// Star represents the '*' path component
type Star struct{}

func (s Star) text(dst *strings.Builder, redact bool) {
	dst.WriteString("*")
}

func (s Star) Equals(e Node) bool {
	_, ok := e.(Star)
	return ok
}

func (s Star) walk(v Visitor) {}

func (s Star) Encode(dst *ion.Buffer, st *ion.Symtab) {
	dst.BeginStruct(-1)
	settype(dst, st, "star")
	dst.EndStruct()
}

func (s Star) SetField(ion.Field) error {
	return errUnexpectedField
}

// Missing represents the MISSING keyword
type Missing struct{}

func (m Missing) text(dst *strings.Builder, redact bool) {
	dst.WriteString("MISSING")
}
func (m Missing) walk(v Visitor) {}

func (m Missing) Type() TypeSet {
	return MissingType
}

func (m Missing) Equals(x Node) bool {
	_, ok := x.(Missing)
	return ok
}

func (m Missing) Encode(dst *ion.Buffer, st *ion.Symtab) {
	dst.BeginStruct(-1)
	settype(dst, st, "missing")
	dst.EndStruct()
}

func (m Missing) SetField(ion.Field) error {
	return errUnexpectedField
}

type Null struct{}

func (n Null) text(dst *strings.Builder, redact bool) {
	dst.WriteString("NULL")
}
func (n Null) walk(v Visitor) {}

func (n Null) Equals(x Node) bool {
	_, ok := x.(Null)
	return ok
}

func (n Null) Type() TypeSet {
	return oneType(ion.NullType)
}

func (n Null) Encode(dst *ion.Buffer, st *ion.Symtab) {
	dst.WriteNull()
}

func (n Null) Datum() ion.Datum {
	return ion.Null
}

// IsKeyword is the function that the expr library
// uses to determine if a string would match as
// a PartiQL keyword.
//
// (Please don't set this yourself; it is set by
// expr/partiql so that they can share keyword tables.)
var IsKeyword func(s string) bool

// QuoteID produces a textual PartiQL identifier;
// the returned string will be double-quoted with escapes
// if it contains non-printable characters or it is a
// PartiQL keyword.
func QuoteID(s string) string {
	if IsKeyword != nil && IsKeyword(s) || strings.ContainsAny(s, "%,~+-!<>=(){}[]:") {
		return strconv.Quote(s)
	}
	for _, r := range s {
		if !strconv.IsPrint(r) {
			return strconv.Quote(s)
		}
	}
	return s
}

// Identifier produces a single-element
// path expression from an identifier string
func Identifier(x string) Ident { return Ident(x) }

// CmpOp is a comparison operation type
type CmpOp int

const (
	Equals CmpOp = iota
	NotEquals

	// note: keep these in order
	// so that we can determine
	// quickly if we are performing
	// an ordinal comparison:

	Less
	LessEquals
	Greater
	GreaterEquals
)

func (c CmpOp) String() string {
	switch c {
	case Equals:
		return "="
	case NotEquals:
		return "<>"
	case Less:
		return "<"
	case LessEquals:
		return "<="
	case Greater:
		return ">"
	case GreaterEquals:
		return ">="
	default:
		return fmt.Sprintf("<CmpOp=%d>", int(c))
	}
}

func (c CmpOp) Ordinal() bool {
	return c >= Less && c <= GreaterEquals
}

// Flip returns the operator that is equivalent to c if
// used with the operand order reversed.
func (c CmpOp) Flip() CmpOp {
	switch c {
	case Less:
		return Greater
	case LessEquals:
		return GreaterEquals
	case Greater:
		return Less
	case GreaterEquals:
		return LessEquals
	default:
		return c // Equals, NotEquals
	}
}

func (c CmpOp) invert() CmpOp {
	switch c {
	case Less:
		return GreaterEquals
	case LessEquals:
		return Greater
	case Greater:
		return LessEquals
	case GreaterEquals:
		return Less
	case Equals:
		return NotEquals
	case NotEquals:
		return Equals
	default:
		return c
	}
}

// Between yields an expression equivalent to
//
//	<val> BETWEEN <lo> AND <hi>
func Between(val, lo, hi Node) *Logical {
	return &Logical{
		Op:    OpAnd,
		Left:  Compare(GreaterEquals, val, lo),
		Right: Compare(LessEquals, val, hi),
	}
}

// Member is an implementation of IN
// that compares against a list of constant
// values, i.e. MEMBER(x, 3, 'foo', ['x', 1.5])
type Member struct {
	Arg Node
	Set ion.Bag
}

func (m *Member) walk(v Visitor) {
	Walk(v, m.Arg)
}

func (m *Member) rewrite(r Rewriter) Node {
	m.Arg = Rewrite(r, m.Arg)
	// not rewriting m.Values, since they
	// are constants and therefore do not
	// have rewrite methods anyway
	return m
}

func bagText(src *ion.Bag, dst *strings.Builder, redact bool) {
	if redact {
		fmt.Fprintf(dst, "'<redacted; %d elements>'", src.Len())
		return
	}
	first := true
	src.Each(func(d ion.Datum) bool {
		if !first {
			dst.WriteString(", ")
		}
		c, ok := AsConstant(d)
		if !ok {
			dst.WriteString("'?'")
		} else {
			c.text(dst, redact)
		}
		first = false
		return true
	})
}

func (m *Member) text(out *strings.Builder, redact bool) {
	m.Arg.text(out, redact)
	out.WriteString(" IN (")
	bagText(&m.Set, out, redact)
	out.WriteString(")")
}

func (m *Member) Encode(dst *ion.Buffer, st *ion.Symtab) {
	dst.BeginStruct(-1)
	settype(dst, st, "member")
	dst.BeginField(st.Intern("arg"))
	m.Arg.Encode(dst, st)
	dst.BeginField(st.Intern("values"))
	dst.BeginList(-1)
	m.Set.Encode(dst, st)
	dst.EndList()
	dst.EndStruct()
}

func (m *Member) SetField(f ion.Field) error {
	var err error
	switch f.Label {
	case "arg":
		m.Arg, err = Decode(f.Datum)
	case "values":
		return m.Set.AddList(f.Datum)
	default:
		return errUnexpectedField
	}
	return err
}

func (m *Member) Equals(e Node) bool {
	me, ok := e.(*Member)
	if !ok || me.Set.Len() != m.Set.Len() {
		return false
	}
	if !m.Arg.Equals(me.Arg) {
		return false
	}
	return m.Set.Equals(&me.Set)
}

func allConst(lst []Node) bool {
	for i := range lst {
		_, ok := lst[i].(Constant)
		if !ok {
			return false
		}
	}
	return true
}

// In yields an expression equivalent to
//
//	<val> IN (cmp ...)
func In(val Node, cmp ...Node) Node {
	if len(cmp) >= 1 && allConst(cmp) {
		mem := &Member{Arg: val}
		for i := range cmp {
			mem.Set.AddDatum(cmp[i].(Constant).Datum())
		}
		return mem
	}

	top := (Node)(Compare(Equals, val, cmp[0]))
	rest := cmp[1:]
	for i := range rest {
		top = Or(top, Compare(Equals, val, rest[i]))
	}
	return top
}

// Lookup is an associative array lookup operation
// against a constant table.
type Lookup struct {
	// Expr is the value to be looked up in Keys,
	// and Else is the value to be returned if
	// there is no corresponding key (or MISSING
	// if Else is nil).
	Expr, Else Node
	// Keys and Values are the corresponding
	// keys and values for the contents of the
	// lookup table.
	Keys, Values ion.Bag
}

func (l *Lookup) Equals(o Node) bool {
	l2, ok := o.(*Lookup)
	return ok && l.Expr.Equals(l2.Expr) &&
		Equivalent(l.Else, l2.Else) &&
		l.Keys.Equals(&l2.Keys) &&
		l.Values.Equals(&l2.Values)
}

func (l *Lookup) walk(v Visitor) {
	Walk(v, l.Expr)
	if l.Else != nil {
		Walk(v, l.Else)
	}
}

func (l *Lookup) rewrite(r Rewriter) Node {
	l.Expr = Rewrite(r, l.Expr)
	if l.Else != nil {
		l.Else = Rewrite(r, l.Else)
	}
	return l
}

func (l *Lookup) text(out *strings.Builder, redact bool) {
	out.WriteString("HASH_LOOKUP(")
	l.Expr.text(out, redact)
	out.WriteString(", [")
	bagText(&l.Keys, out, redact)
	out.WriteString("], [")
	bagText(&l.Values, out, redact)
	out.WriteString("]")
	if l.Else != nil {
		out.WriteString(", ")
		l.Else.text(out, redact)
	}
	out.WriteString(")")
}

func (l *Lookup) Encode(dst *ion.Buffer, st *ion.Symtab) {
	dst.BeginStruct(-1)
	settype(dst, st, "lookup")
	dst.BeginField(st.Intern("expr"))
	l.Expr.Encode(dst, st)
	if l.Else != nil {
		dst.BeginField(st.Intern("else"))
		l.Else.Encode(dst, st)
	}
	dst.BeginField(st.Intern("keys"))
	dst.BeginList(-1)
	l.Keys.Encode(dst, st)
	dst.EndList()
	dst.BeginField(st.Intern("values"))
	dst.BeginList(-1)
	l.Values.Encode(dst, st)
	dst.EndList()
	dst.EndStruct()
}

func (l *Lookup) SetField(f ion.Field) error {
	var err error
	switch f.Label {
	case "expr":
		l.Expr, err = Decode(f.Datum)
	case "else":
		l.Else, err = Decode(f.Datum)
	case "keys":
		l.Keys.AddList(f.Datum)
	case "values":
		l.Values.AddList(f.Datum)
	default:
		return errUnexpectedField
	}
	return err
}

type Comparison struct {
	Op          CmpOp
	Left, Right Node
}

func (c *Comparison) Equals(x Node) bool {
	ec, ok := x.(*Comparison)
	return ok && ec.Op == c.Op && c.Left.Equals(ec.Left) && c.Right.Equals(ec.Right)
}

func (c *Comparison) Encode(dst *ion.Buffer, st *ion.Symtab) {
	dst.BeginStruct(-1)
	settype(dst, st, "cmp")
	dst.BeginField(st.Intern("op"))
	dst.WriteUint(uint64(c.Op))
	dst.BeginField(st.Intern("left"))
	c.Left.Encode(dst, st)
	dst.BeginField(st.Intern("right"))
	c.Right.Encode(dst, st)
	dst.EndStruct()
}

func (c *Comparison) SetField(f ion.Field) error {
	switch f.Label {
	case "op":
		u, err := f.Uint()
		if err != nil {
			return err
		}
		c.Op = CmpOp(u)
	case "left":
		var err error
		c.Left, err = Decode(f.Datum)
		return err
	case "right":
		var err error
		c.Right, err = Decode(f.Datum)
		return err
	default:
		return errUnexpectedField
	}
	return nil
}

// StringMatchOp is one of the string-matching operations
// (LIKE, ILIKE, SIMILAR TO, ...)
type StringMatchOp int

const (
	Like          StringMatchOp = iota // LIKE <literal> (also ~~)
	Ilike                              // ILIKE <literal> (also ~~*)
	SimilarTo                          // SIMILAR TO <literal>
	RegexpMatch                        // ~ <literal>
	RegexpMatchCi                      // ~* <literal> case-insensitive regex match
)

func (s StringMatchOp) String() string {
	switch s {
	case Like:
		return "LIKE"
	case Ilike:
		return "ILIKE"
	case SimilarTo:
		return "SIMILAR TO"
	case RegexpMatch:
		return "~"
	case RegexpMatchCi:
		return "~*"
	default:
		return fmt.Sprintf("<StringMatchOp=%d>", int(s))
	}
}

// StringMatch is an expression that matches an
// arbitrary value against a literal string pattern.
type StringMatch struct {
	Op      StringMatchOp
	Expr    Node
	Pattern string
	Escape  string
}

func (s *StringMatch) Equals(x Node) bool {
	o, ok := x.(*StringMatch)
	return ok &&
		o.Op == s.Op &&
		o.Expr.Equals(s.Expr) &&
		o.Pattern == s.Pattern &&
		o.Escape == s.Escape
}

func (s *StringMatch) Encode(dst *ion.Buffer, st *ion.Symtab) {
	dst.BeginStruct(-1)
	settype(dst, st, "stringmatch")
	dst.BeginField(st.Intern("op"))
	dst.WriteInt(int64(s.Op))
	dst.BeginField(st.Intern("expr"))
	s.Expr.Encode(dst, st)
	dst.BeginField(st.Intern("pattern"))
	dst.WriteString(s.Pattern)
	if s.Escape != "" {
		dst.BeginField(st.Intern("escape"))
		dst.WriteString(s.Escape)
	}
	dst.EndStruct()
}

func (s *StringMatch) SetField(f ion.Field) error {
	var err error
	switch f.Label {
	case "op":
		v := int64(0)
		v, err = f.Int()
		s.Op = StringMatchOp(v)
	case "expr":
		s.Expr, err = Decode(f.Datum)
	case "pattern":
		s.Pattern, err = f.String()
	case "escape":
		s.Escape, err = f.String()
	default:
		return errUnexpectedField
	}
	return err
}

func (s *StringMatch) walk(v Visitor) { Walk(v, s.Expr) }

func (s *StringMatch) rewrite(r Rewriter) Node {
	s.Expr = Rewrite(r, s.Expr)
	return s
}

func (s *StringMatch) text(dst *strings.Builder, redact bool) {
	s.Expr.text(dst, redact)
	fmt.Fprintf(dst, " %s ", s.Op)
	quote(dst, s.Pattern)
	if s.Escape != "" && s.Escape != string(stringext.NoEscape) {
		dst.WriteString(" ESCAPE ")
		quote(dst, s.Escape)
	}
}

// Not yields
//
//	! (Expr)
type Not struct {
	Expr Node
}

func (n *Not) text(dst *strings.Builder, redact bool) {
	dst.WriteString("!(")
	n.Expr.text(dst, redact)
	dst.WriteByte(')')
}

func (n *Not) walk(v Visitor) {
	Walk(v, n.Expr)
}

func (n *Not) rewrite(r Rewriter) Node {
	n.Expr = Rewrite(r, n.Expr)
	return n
}

func (n *Not) invert() Node {
	return n.Expr
}

func (n *Not) Type() TypeSet {
	return LogicalType
}

func (n *Not) Equals(x Node) bool {
	xn, ok := x.(*Not)
	return ok && n.Expr.Equals(xn.Expr)
}

func (n *Not) Encode(dst *ion.Buffer, st *ion.Symtab) {
	dst.BeginStruct(-1)
	settype(dst, st, "not")
	dst.BeginField(st.Intern("inner"))
	n.Expr.Encode(dst, st)
	dst.EndStruct()
}

func (n *Not) SetField(f ion.Field) error {
	switch f.Label {
	case "inner":
		var err error
		n.Expr, err = Decode(f.Datum)
		return err
	}
	return errUnexpectedField
}

// Compare generates a comparison operation
// of the given type and with the given arguments
func Compare(op CmpOp, left, right Node) Node {
	return &Comparison{Op: op, Left: left, Right: right}
}

func (c *Comparison) walk(v Visitor) {
	if c.Left != nil {
		Walk(v, c.Left)
	}
	if c.Right != nil {
		Walk(v, c.Right)
	}
}

func (c *Comparison) rewrite(r Rewriter) Node {
	c.Left = Rewrite(r, c.Left)
	c.Right = Rewrite(r, c.Right)
	return c
}

func (c *Comparison) text(dst *strings.Builder, redact bool) {
	parens := false
	// if the right-hand-side op is also
	// a comparison, we must parenthesize it,
	// since otherwise the left-hand associativity
	// would change the meaning of the expression
	// without parentheses
	// i.e.
	//   A = B = C is (A = B) = C
	// so if we have
	//   A = (B = C)
	// then we must use parentheses
	//
	// arithmetic expressions are fine
	// on the rhs, because they have higher precedence
	if _, ok := c.Right.(*Comparison); ok {
		parens = true
	}
	// similarly, if we are comparing boolean
	// expressions with =/<>, make sure those are wrapped
	// as they have lower precedence than comparisons
	if _, ok := c.Right.(*Logical); ok {
		parens = true
	}
	if _, ok := c.Left.(*Logical); ok {
		dst.WriteByte('(')
		c.Left.text(dst, redact)
		dst.WriteByte(')')
	} else {
		c.Left.text(dst, redact)
	}
	fmt.Fprintf(dst, " %s ", c.Op)
	if parens {
		dst.WriteByte('(')
	}
	c.Right.text(dst, redact)
	if parens {
		dst.WriteByte(')')
	}
}

func (c *Comparison) Type() TypeSet {
	return LogicalType
}

func (c *Comparison) invert() Node {
	return &Comparison{
		Op:    c.Op.invert(),
		Left:  c.Left,
		Right: c.Right,
	}
}

// LogicalOp is a logical operation
type LogicalOp int

const (
	OpAnd  LogicalOp = iota // A AND B
	OpOr                    // A OR B
	OpXnor                  // A XNOR B (A = B)
	OpXor                   // A XOR B (A != B)
)

func (l LogicalOp) String() string {
	switch l {
	case OpAnd:
		return "AND"
	case OpOr:
		return "OR"
	case OpXor:
		return "XOR"
	case OpXnor:
		return "XNOR"
	default:
		return fmt.Sprintf("<LogicalOp=%d>", int(l))
	}
}

// Logical is a Node that represents
// a logical expression
type Logical struct {
	Op          LogicalOp
	Left, Right Node
}

func (l *Logical) Equals(x Node) bool {
	xl, ok := x.(*Logical)
	return ok && l.Op == xl.Op && l.Left.Equals(xl.Left) && l.Right.Equals(xl.Right)
}

func (l *Logical) walk(v Visitor) {
	if l.Left != nil {
		Walk(v, l.Left)
	}
	if l.Right != nil {
		Walk(v, l.Right)
	}
}

func (l *Logical) rewrite(r Rewriter) Node {
	l.Left = Rewrite(r, l.Left)
	l.Right = Rewrite(r, l.Right)
	return l
}

func (l *Logical) text(dst *strings.Builder, redact bool) {
	parens := false
	// if we don't parenthesize the rhs expression
	// when it is an infix logical operation, we
	// will produce an expression that means something
	// different when interpreted with left-associative rules
	//
	// arithmetic and comparison expressions
	// have higher precedence, so we don't need to wrap them
	if _, ok := l.Right.(*Logical); ok {
		parens = true
	}
	l.Left.text(dst, redact)
	var middle string
	switch l.Op {
	case OpAnd:
		middle = " AND "
	case OpOr:
		middle = " OR "
	case OpXor:
		middle = " <> "
	case OpXnor:
		middle = " = "
	default:
		middle = "Logical(???)"
	}
	dst.WriteString(middle)
	if parens {
		dst.WriteByte('(')
	}
	l.Right.text(dst, redact)
	if parens {
		dst.WriteByte(')')
	}
}

func (l *Logical) Type() TypeSet {
	return LogicalType
}

func (l *Logical) Encode(dst *ion.Buffer, st *ion.Symtab) {
	dst.BeginStruct(-1)
	settype(dst, st, "logical")
	dst.BeginField(st.Intern("op"))
	dst.WriteUint(uint64(l.Op))
	dst.BeginField(st.Intern("left"))
	l.Left.Encode(dst, st)
	dst.BeginField(st.Intern("right"))
	l.Right.Encode(dst, st)
	dst.EndStruct()
}

func (l *Logical) SetField(f ion.Field) error {
	var err error
	switch f.Label {
	case "op":
		var u uint64
		u, err = f.Uint()
		l.Op = LogicalOp(u)
	case "left":
		l.Left, err = Decode(f.Datum)
	case "right":
		l.Right, err = Decode(f.Datum)
	default:
		return errUnexpectedField
	}
	return err
}

// And yields '<left> AND <right>'
func And(left, right Node) *Logical {
	return &Logical{Op: OpAnd, Left: left, Right: right}
}

// Or yields '<left> OR <right>'
func Or(left, right Node) *Logical {
	return &Logical{Op: OpOr, Left: left, Right: right}
}

// Xor computes 'left XOR right',
// which is equivalent to 'left <> right'
// for boolean expressions
func Xor(left, right Node) *Logical {
	return &Logical{Op: OpXor, Left: left, Right: right}
}

// Xnor computes 'left XNOR right',
// which is equivalent to 'left = right'
// for boolean expressions.
func Xnor(left, right Node) *Logical {
	return &Logical{Op: OpXnor, Left: left, Right: right}
}

// Call yields op(args...).
func Call(op BuiltinOp, args ...Node) *Builtin {
	return &Builtin{Func: op, Text: op.String(), Args: args}
}

// CallByName yields 'fn(args...)'
// Use Call when you know the BuiltinOp associated with fn.
func CallByName(fn string, args ...Node) *Builtin {
	var text string
	op := name2Builtin(strings.ToUpper(fn))
	if op != Unspecified {
		text = op.String()
	} else {
		text = fn
	}

	return &Builtin{Func: op, Text: text, Args: args}
}

// Builtin is a Node that represents
// a call to a builtin function
type Builtin struct {
	Func BuiltinOp // function name
	Text string    // actual text provided to Call
	Args []Node    // function arguments
}

func (b *Builtin) walk(v Visitor) {
	for i := range b.Args {
		Walk(v, b.Args[i])
	}
}

func (b *Builtin) rewrite(r Rewriter) Node {
	for i := range b.Args {
		b.Args[i] = Rewrite(r, b.Args[i])
	}
	return b
}

func (b *Builtin) Equals(x Node) bool {
	xb, ok := x.(*Builtin)
	if ok && b.Func == xb.Func && len(b.Args) == len(xb.Args) {
		for i := range b.Args {
			if !b.Args[i].Equals(xb.Args[i]) {
				return false
			}
		}
		return true
	}
	return false
}

func (b *Builtin) Name() string {
	if b.Func != Unspecified {
		return b.Func.String()
	}
	return strings.ToUpper(b.Text)
}

func (b *Builtin) SetField(f ion.Field) error {
	switch f.Label {
	case "func":
		str, err := f.String()
		b.Func = name2Builtin(str)
		if b.Func == Unspecified {
			b.Text = str
		}
		return err
	case "args":
		return f.UnpackList(func(d ion.Datum) error {
			nod, err := Decode(d)
			if err != nil {
				return err
			}
			b.Args = append(b.Args, nod)
			return nil
		})
	default:
		return errUnexpectedField
	}
}

func (b *Builtin) text(dst *strings.Builder, redact bool) {
	if info := b.info(); info != nil && info.text != nil {
		info.text(b.Args, dst, redact)
		return
	}
	dst.WriteString(b.Name())
	dst.WriteByte('(')
	for i := range b.Args {
		b.Args[i].text(dst, redact)
		if i != len(b.Args)-1 {
			dst.WriteString(", ")
		}
	}
	dst.WriteByte(')')
}

func (b *Builtin) Encode(dst *ion.Buffer, st *ion.Symtab) {
	dst.BeginStruct(-1)
	settype(dst, st, "builtin")
	dst.BeginField(st.Intern("func"))
	dst.WriteString(b.Name())
	dst.BeginField(st.Intern("args"))
	dst.BeginList(-1)
	for i := range b.Args {
		b.Args[i].Encode(dst, st)
	}
	dst.EndList()
	dst.EndStruct()
}

// UnaryArithOp is one of the unary arithmetic ops
// (unary negation, trunc, floor, etc.)
type UnaryArithOp int

const (
	NegOp UnaryArithOp = iota
	BitNotOp
)

type UnaryArith struct {
	Op    UnaryArithOp
	Child Node
}

func NewUnaryArith(op UnaryArithOp, child Node) *UnaryArith {
	return &UnaryArith{Op: op, Child: child}
}

func (u *UnaryArith) text(dst *strings.Builder, redact bool) {
	var pre string
	switch u.Op {
	case NegOp:
		pre = "-"
	case BitNotOp:
		pre = "~"
	default:
		pre = "UNKNOWN_FUNCTION"
	}
	dst.WriteString(pre)
	dst.WriteByte('(')
	u.Child.text(dst, redact)
	dst.WriteByte(')')
}

func Neg(child Node) *UnaryArith {
	return NewUnaryArith(NegOp, child)
}

func BitNot(child Node) *UnaryArith {
	return NewUnaryArith(BitNotOp, child)
}

func (u *UnaryArith) walk(v Visitor) {
	if u.Child != nil {
		Walk(v, u.Child)
	}
}

func (u *UnaryArith) rewrite(r Rewriter) Node {
	u.Child = Rewrite(r, u.Child)
	return u
}

func (u *UnaryArith) Equals(x Node) bool {
	xu, ok := x.(*UnaryArith)
	return ok && u.Op == xu.Op && u.Child.Equals(xu.Child)
}

func (u *UnaryArith) Encode(dst *ion.Buffer, st *ion.Symtab) {
	dst.BeginStruct(-1)
	settype(dst, st, "unaryArith")
	dst.BeginField(st.Intern("op"))
	dst.WriteUint(uint64(u.Op))
	dst.BeginField(st.Intern("child"))
	u.Child.Encode(dst, st)
	dst.EndStruct()
}

func (u *UnaryArith) SetField(f ion.Field) error {
	var err error

	switch f.Label {
	case "op":
		var val uint64
		val, err = f.Uint()
		u.Op = UnaryArithOp(val)
	case "child":
		u.Child, err = Decode(f.Datum)
	default:
		return errUnexpectedField
	}

	return err
}

func (u *UnaryArith) typeof(hint Hint) TypeSet {
	// The return type is Numeric, but it's also MISSING if Child is MISSING.
	nt := NumericType
	ct := TypeOf(u.Child, hint)

	// The result can be MISSING if Child can be MISSING or a non-numeric type.
	nt |= (MissingType & ct)
	if ct&^(MissingType|NumericType) != 0 {
		nt |= MissingType
	}
	return nt
}

type ArithOp int

const (
	AddOp ArithOp = iota
	SubOp
	MulOp
	DivOp
	ModOp
	BitAndOp
	BitOrOp
	BitXorOp
	ShiftLeftLogicalOp
	ShiftRightArithmeticOp
	ShiftRightLogicalOp
)

func (a ArithOp) String() string {
	switch a {
	case AddOp:
		return "+"
	case SubOp:
		return "-"
	case MulOp:
		return "*"
	case DivOp:
		return "/"
	case ModOp:
		return "%"
	case BitAndOp:
		return "&"
	case BitOrOp:
		return "|"
	case BitXorOp:
		return "^"
	case ShiftLeftLogicalOp:
		return "<<"
	case ShiftRightArithmeticOp:
		return ">>"
	case ShiftRightLogicalOp:
		return ">>>"
	default:
		return fmt.Sprintf("<ArithOp=%d>", int(a))
	}
}

// Arithmetic is a binary arithmetic expression
type Arithmetic struct {
	Op          ArithOp
	Left, Right Node
}

// NewArith generates a binary arithmetic expression.
func NewArith(op ArithOp, left, right Node) *Arithmetic {
	return &Arithmetic{Op: op, Left: left, Right: right}
}

func Add(left, right Node) *Arithmetic    { return NewArith(AddOp, left, right) }
func Sub(left, right Node) *Arithmetic    { return NewArith(SubOp, left, right) }
func Mul(left, right Node) *Arithmetic    { return NewArith(MulOp, left, right) }
func Div(left, right Node) *Arithmetic    { return NewArith(DivOp, left, right) }
func Mod(left, right Node) *Arithmetic    { return NewArith(ModOp, left, right) }
func BitAnd(left, right Node) *Arithmetic { return NewArith(BitAndOp, left, right) }
func BitOr(left, right Node) *Arithmetic  { return NewArith(BitOrOp, left, right) }
func BitXor(left, right Node) *Arithmetic { return NewArith(BitXorOp, left, right) }

func ShiftLeftLogical(left, right Node) *Arithmetic {
	return NewArith(ShiftLeftLogicalOp, left, right)
}

func ShiftRightArithmetic(left, right Node) *Arithmetic {
	return NewArith(ShiftRightArithmeticOp, left, right)
}

func ShiftRightLogical(left, right Node) *Arithmetic {
	return NewArith(ShiftRightLogicalOp, left, right)
}

func infix(e Node) bool {
	if _, ok := e.(*Arithmetic); ok {
		return true
	}
	_, ok := e.(*Comparison)
	return ok
}

func (a *Arithmetic) text(dst *strings.Builder, redact bool) {
	// if the right-hand-side expression
	// is an infix binary expression, then
	// we must parenthesize it in case it contains
	// an operator of higher precedence
	// (we could compare precedence directly,
	// but it's easier just to do this unconditionally)
	parens := infix(a.Right)
	a.Left.text(dst, redact)
	fmt.Fprintf(dst, " %s ", a.Op)
	if parens {
		dst.WriteByte('(')
	}
	a.Right.text(dst, redact)
	if parens {
		dst.WriteByte(')')
	}
}

func (a *Arithmetic) walk(v Visitor) {
	if a.Left != nil {
		Walk(v, a.Left)
	}
	if a.Right != nil {
		Walk(v, a.Right)
	}
}

func (a *Arithmetic) rewrite(r Rewriter) Node {
	a.Left = Rewrite(r, a.Left)
	a.Right = Rewrite(r, a.Right)
	return a
}

func (a *Arithmetic) Equals(x Node) bool {
	xa, ok := x.(*Arithmetic)
	if !ok {
		return false
	}

	return a.Op == xa.Op && a.Left.Equals(xa.Left) && a.Right.Equals(xa.Right)
}

func (a *Arithmetic) Encode(dst *ion.Buffer, st *ion.Symtab) {
	dst.BeginStruct(-1)
	settype(dst, st, "arith")
	dst.BeginField(st.Intern("op"))
	dst.WriteUint(uint64(a.Op))
	dst.BeginField(st.Intern("left"))
	a.Left.Encode(dst, st)
	dst.BeginField(st.Intern("right"))
	a.Right.Encode(dst, st)
	dst.EndStruct()
}

func (a *Arithmetic) SetField(f ion.Field) error {
	var err error
	switch f.Label {
	case "op":
		var u uint64
		u, err = f.Uint()
		a.Op = ArithOp(u)
	case "left":
		a.Left, err = Decode(f.Datum)
	case "right":
		a.Right, err = Decode(f.Datum)
	default:
		return errUnexpectedField
	}
	return err
}

func (a *Arithmetic) typeof(hint Hint) TypeSet {
	// the return type is Numeric,
	// but it is also Missing if either
	// the left or right value can be
	// missing
	left := TypeOf(a.Left, hint)
	if left&NumericType == 0 {
		return MissingType
	}
	right := TypeOf(a.Right, hint)
	if right&NumericType == 0 {
		return MissingType
	}
	both := (left | right)
	if (a.Op == DivOp || a.Op == ModOp) || (both&^NumericType) != 0 {
		// div & mod are unusual; divide/modulo-by-zero
		// can yield missing even if both inputs are
		// always numbers
		//
		// similarly, if either input can be
		// a non-numeric value, we'd expect
		// to get MISSING as well
		both |= MissingType
	}
	return both & (NumericType | MissingType)
}

func Append(left, right Node) *Appended {
	a := &Appended{}
	a.append(left)
	a.append(right)
	return a
}

// Appended is an append (++) expression
type Appended struct {
	Values []Node
}

func (a *Appended) append(x Node) {
	if a2, ok := x.(*Appended); ok {
		a.Values = append(a.Values, a2.Values...)
	} else {
		a.Values = append(a.Values, x)
	}
}

func (a *Appended) text(dst *strings.Builder, redact bool) {
	if len(a.Values) > 1 {
		dst.WriteByte('(')
	}
	for i := range a.Values {
		if i > 0 {
			dst.WriteString(" ++ ")
		}
		a.Values[i].text(dst, redact)
	}
	if len(a.Values) > 1 {
		dst.WriteByte(')')
	}
}

func (a *Appended) rewrite(r Rewriter) Node {
	for i := range a.Values {
		a.Values[i] = Rewrite(r, a.Values[i])
	}
	return a
}

func (a *Appended) Equals(x Node) bool {
	a2, ok := x.(*Appended)
	if !ok || len(a.Values) != len(a2.Values) {
		return false
	}
	for i := range a.Values {
		if !a.Values[i].Equals(a2.Values[i]) {
			return false
		}
	}
	return true
}

func (a *Appended) Encode(dst *ion.Buffer, st *ion.Symtab) {
	dst.BeginStruct(-1)
	settype(dst, st, "append")
	dst.BeginField(st.Intern("values"))
	dst.BeginList(-1)
	for i := range a.Values {
		a.Values[i].Encode(dst, st)
	}
	dst.EndList()
	dst.EndStruct()
}

func (a *Appended) SetField(f ion.Field) error {
	switch f.Label {
	case "values":
		return f.UnpackList(func(d ion.Datum) error {
			v, err := Decode(d)
			if err != nil {
				return err
			}
			a.append(v)
			return nil
		})
	default:
		return errUnexpectedField
	}
}

func (a *Appended) walk(v Visitor) {
	for i := range a.Values {
		Walk(v, a.Values[i])
	}
}

type Keyword int

const (
	IsNull Keyword = iota
	IsNotNull
	IsMissing
	IsNotMissing
	IsTrue
	IsNotTrue
	IsFalse
	IsNotFalse
)

func (k Keyword) text(dst *strings.Builder, redact bool) {
	switch k {
	case IsNull:
		dst.WriteString("NULL")
	case IsNotNull:
		dst.WriteString("NOT NULL")
	case IsMissing:
		dst.WriteString("MISSING")
	case IsNotMissing:
		dst.WriteString("NOT MISSING")
	case IsTrue:
		dst.WriteString("TRUE")
	case IsNotTrue:
		dst.WriteString("NOT TRUE")
	case IsFalse:
		dst.WriteString("FALSE")
	case IsNotFalse:
		dst.WriteString("NOT FALSE")
	default:
		dst.WriteString("???")
	}
}

type IsKey struct {
	Expr Node
	Key  Keyword
}

func (i *IsKey) text(dst *strings.Builder, redact bool) {
	i.Expr.text(dst, redact)
	dst.WriteString(" IS ")
	i.Key.text(dst, redact)
}

func (i *IsKey) walk(v Visitor) {
	Walk(v, i.Expr)
}

func (i *IsKey) rewrite(r Rewriter) Node {
	i.Expr = Rewrite(r, i.Expr)
	return i
}

func (i *IsKey) Type() TypeSet {
	// IS, unlike comparison operations,
	// *always* returns a TRUE or FALSE value
	return oneType(ion.BoolType)
}

func (i *IsKey) Equals(x Node) bool {
	xi, ok := x.(*IsKey)
	return ok && i.Key == xi.Key && i.Expr.Equals(xi.Expr)
}

func (i *IsKey) Encode(dst *ion.Buffer, st *ion.Symtab) {
	dst.BeginStruct(-1)
	settype(dst, st, "is")
	dst.BeginField(st.Intern("key"))
	dst.WriteUint(uint64(i.Key))
	dst.BeginField(st.Intern("inner"))
	i.Expr.Encode(dst, st)
	dst.EndStruct()
}

func (i *IsKey) SetField(f ion.Field) error {
	var err error
	switch f.Label {
	case "key":
		var u uint64
		u, err = f.Uint()
		i.Key = Keyword(u)
	case "inner":
		i.Expr, err = Decode(f.Datum)
	default:
		return errUnexpectedField
	}
	return err
}

// Is yields
//
//	<e> IS <k>
func Is(e Node, k Keyword) *IsKey {
	return &IsKey{Expr: e, Key: k}
}

func (i *IsKey) invert() Node {
	out := new(IsKey)
	*out = *i
	switch i.Key {
	case IsNull:
		out.Key = IsNotNull
	case IsNotNull:
		out.Key = IsNull
	case IsMissing:
		out.Key = IsNotMissing
	case IsNotMissing:
		out.Key = IsMissing
	case IsTrue:
		out.Key = IsNotTrue
	case IsFalse:
		out.Key = IsNotFalse
	case IsNotTrue:
		out.Key = IsTrue
	case IsNotFalse:
		out.Key = IsFalse
	}
	return out
}

// ParsePath parses simple path expressions
// like 'a.b.z' or 'a[0].y', etc.
func ParsePath(x string) (Node, error) {
	var cur Node
	pushfield := func(s string) {
		s = strings.Trim(s, `"`) // unquote
		if cur == nil {
			cur = Ident(s)
		} else {
			cur = &Dot{
				Inner: cur,
				Field: s,
			}
		}
	}
	pushindex := func(i int) {
		cur = &Index{Inner: cur, Offset: i}
	}
	const (
		parsingField  = 0
		parsingIndex  = 1
		parsingEither = 2
	)
	state := parsingField
	var field []byte
	for len(x) > 0 {
		switch state {
		case parsingEither:
			if x[0] == '[' {
				state = parsingIndex
			} else {
				state = parsingField
			}
		case parsingField:
			if x[0] == '.' || x[0] == '[' {
				if len(field) == 0 {
					return nil, fmt.Errorf("zero-length field in %q not supported", x)
				}
				pushfield(string(field))
				field = field[:0]
				if x[0] == '[' {
					state = parsingIndex
				}
			} else {
				field = append(field, x[0])
			}
		case parsingIndex:
			if x[0] == ']' {
				str := string(field)
				i, err := strconv.Atoi(str)
				if err != nil {
					return nil, fmt.Errorf("bad index: %w", err)
				}
				pushindex(i)
				// after an indexing expression,
				// we can have either another
				// index (i.e. x[0][0])
				// or a field expression
				state = parsingEither
				field = field[:0]
			} else {
				field = append(field, x[0])
			}
		default:
			panic("unreachable")
		}
		x = x[1:]
	}
	// we may encounter the end
	// of the expression while parsing
	// a field name, but *not* an index
	// (which must be terminated by ']')
	if state == parsingField {
		if len(field) == 0 {
			return nil, fmt.Errorf("ParsePath: unterminated field expression")
		}
		pushfield(string(field))
	}
	if state == parsingIndex && len(field) != 0 {
		return nil, fmt.Errorf("ParsePath: unterminated index expression")
	}
	return cur, nil
}

// ParseBindings parses a comma-separated
// list of path expressions with (optional)
// binding parameters.
//
// For example
//
//	a.b as b, a.x[3] as foo
//
// is parsed into two Binding structures
// with the path expressions 'a.b' and 'a.x[3]'
func ParseBindings(str string) ([]Binding, error) {
	sep := strings.Split(str, ",")
	out := make([]Binding, 0, len(sep))
	for i := range sep {
		fields := strings.Fields(sep[i])
		start := fields[0]
		p, err := ParsePath(start)
		if err != nil {
			return nil, fmt.Errorf("binding expression %d: %w", i, err)
		}
		switch len(fields) {
		case 1:
			out = append(out, Bind(p, ""))
		case 3:
			if as := fields[1]; as != "as" && as != "AS " {
				return nil, fmt.Errorf("binding %d: unexpected string %q", i, as)
			}
			out = append(out, Bind(p, fields[2]))
		default:
			return nil, fmt.Errorf("unexpected binding expression %q", fields)
		}
	}
	return out, nil
}

// CaseLimb is one 'WHEN expr THEN expr'
// case limb.
type CaseLimb struct {
	When, Then Node
}

// Case represents a CASE expression.
type Case struct {
	// Limbs are each of the case limbs.
	// There ought to be at least one.
	Limbs []CaseLimb
	// Else is the ELSE limb of the case
	// expression, or nil if no ELSE was specified.
	Else Node

	// Valence is a hint passed to
	// the expression-compilation code
	// regarding the result type of
	// the case expression. Some optimizations
	// make the valence of the CASE obvious.
	Valence string
}

func (c *Case) text(dst *strings.Builder, redact bool) {
	dst.WriteString("CASE")
	for i := range c.Limbs {
		dst.WriteString(" WHEN ")
		c.Limbs[i].When.text(dst, redact)
		dst.WriteString(" THEN ")
		c.Limbs[i].Then.text(dst, redact)
	}
	if c.Else != nil {
		dst.WriteString(" ELSE ")
		c.Else.text(dst, redact)
	}
	dst.WriteString(" END")
}

// IsPathLimbs returns true if the
// ELSE value and every THEN limb
// of the CASE expression is a
// Path expression, Null, or Missing.
func (c *Case) IsPathLimbs() bool {
	ok := func(e Node) bool {
		if IsPath(e) {
			return true
		}
		switch e.(type) {
		case Null:
			return true
		case Missing:
			return true
		default:
			return false
		}
	}
	if c.Else != nil && !ok(c.Else) {
		return false
	}
	for i := range c.Limbs {
		if !ok(c.Limbs[i].Then) {
			return false
		}
	}
	return true
}

func (c *Case) walk(v Visitor) {
	for i := range c.Limbs {
		Walk(v, c.Limbs[i].When)
		Walk(v, c.Limbs[i].Then)
	}
	if c.Else != nil {
		Walk(v, c.Else)
	}
}

func (c *Case) rewrite(r Rewriter) Node {
	for i := range c.Limbs {
		c.Limbs[i].When = Rewrite(r, c.Limbs[i].When)
		c.Limbs[i].Then = Rewrite(r, c.Limbs[i].Then)
	}
	if c.Else != nil {
		c.Else = Rewrite(r, c.Else)
	}
	return c
}

func (c *Case) Equals(e Node) bool {
	oc, ok := e.(*Case)
	if !ok {
		return false
	}
	if len(c.Limbs) != len(oc.Limbs) {
		return false
	}
	if (c.Else != nil) != (oc.Else != nil) {
		return false
	}
	for i := range c.Limbs {
		if !c.Limbs[i].When.Equals(oc.Limbs[i].When) {
			return false
		}
		if !c.Limbs[i].Then.Equals(oc.Limbs[i].Then) {
			return false
		}
	}
	if c.Else != nil {
		return c.Else.Equals(oc.Else)
	}
	return true
}

func (c *Case) Encode(dst *ion.Buffer, st *ion.Symtab) {
	dst.BeginStruct(-1)
	settype(dst, st, "case")
	dst.BeginField(st.Intern("limbs"))
	// encode limbs as
	// [[when, then] ...]
	dst.BeginList(-1)
	for i := range c.Limbs {
		dst.BeginList(-1)
		c.Limbs[i].When.Encode(dst, st)
		c.Limbs[i].Then.Encode(dst, st)
		dst.EndList()
	}
	dst.EndList()
	if c.Else != nil {
		dst.BeginField(st.Intern("else"))
		c.Else.Encode(dst, st)
	}
	if c.Valence != "" {
		dst.BeginField(st.Intern("valence"))
		dst.WriteString(c.Valence)
	}
	dst.EndStruct()
}

func (c *Case) SetField(f ion.Field) error {
	var err error
	switch f.Label {
	case "limbs":
		return f.UnpackList(func(d ion.Datum) error {
			i, err := d.Iterator()
			if err != nil {
				return err
			}
			d, err = i.Next()
			if err != nil {
				return err
			}
			when, err := Decode(d)
			if err != nil {
				return err
			}
			d, err = i.Next()
			if err != nil {
				return err
			}
			then, err := Decode(d)
			if err != nil {
				return err
			}
			c.Limbs = append(c.Limbs, CaseLimb{When: when, Then: then})
			return nil
		})
	case "else":
		c.Else, err = Decode(f.Datum)
	case "valence":
		c.Valence, err = f.String()
	default:
		return errUnexpectedField
	}
	return err
}

// Coalesce turns COALESCE(args...)
// into an equivalent Case expression.
func Coalesce(nodes []Node) *Case {
	c := &Case{Limbs: make([]CaseLimb, len(nodes)), Else: Null{}}
	for i := range c.Limbs {
		c.Limbs[i].When = Is(nodes[i], IsNotNull)
		c.Limbs[i].Then = nodes[i]
	}
	return c
}

// NullIf implements SQL NULLIF(a, b);
// it is transformed into an equivalent CASE expression:
//
//	CASE WHEN a = b THEN NULL ELSE a
func NullIf(a, b Node) Node {
	return IfThenElse(Compare(Equals, a, b), Null{}, a)
}

func (c *Case) typeof(h Hint) TypeSet {
	// just compute the union type
	// of every WHEN clause, plus ELSE
	//
	// FIXME: we can provide more precise
	// type information if we correlate the
	// fact that each WHEN probably indicates
	// some additional type information
	// that must be true inside THEN;
	// for example
	//   WHEN i < 3 THEN i
	// tells us that 'i' is numeric
	out := TypeSet(0)
	for i := range c.Limbs {
		out |= TypeOf(c.Limbs[i].Then, h)
	}
	if c.Else != nil {
		return out | TypeOf(c.Else, h)
	}
	return out | NullType
}

// IfThenElse ternary conditional. eg. result := (count=0) ? thenExpr : elseExpr
// is written as IfThenElse(Compare(Equals, count, Integer(0)), thenExpr, elseExpr)
func IfThenElse(whenExpr, thenExpr, elseExpr Node) Node {
	result := Case{
		Limbs: []CaseLimb{{
			When: whenExpr,
			Then: thenExpr,
		}},
		Else: elseExpr,
	}
	return &result
}

// Cast represents a CAST(... AS ...) expression.
type Cast struct {
	// From is the expression on the left-hand-side of the CAST.
	From Node
	// To is the representation of the constant on the right-hand-side
	// of the CAST expression.
	// Typically, only one bit of the TypeSet is present, to indicate
	// the desired result type.
	To TypeSet
}

// TargetTypeName returns the name of the target type.
func (c *Cast) TargetTypeName() string {
	switch c.To {
	case MissingType:
		return "MISSING"
	case NullType:
		return "NULL"
	case StringType:
		return "STRING"
	case IntegerType:
		return "INTEGER"
	case FloatType:
		return "FLOAT"
	case BoolType:
		return "BOOLEAN"
	case TimeType:
		return "TIMESTAMP"
	case StructType:
		return "STRUCT"
	case ListType:
		return "LIST"
	case DecimalType:
		return "DECIMAL"
	case SymbolType:
		return "SYMBOL"
	default:
		return "UNKNOWN"
	}
}

func (c *Cast) text(dst *strings.Builder, redact bool) {
	dst.WriteString("CAST(")
	c.From.text(dst, redact)
	dst.WriteString(" AS ")
	dst.WriteString(c.TargetTypeName())
	dst.WriteByte(')')
}

func (c *Cast) typeof(h Hint) TypeSet {
	ft := TypeOf(c.From, h)
	if ft&c.To == 0 {
		return MissingType
	}
	out := c.To
	if ft&c.To != ft {
		out |= MissingType
	}
	return out
}

func (c *Cast) walk(v Visitor) {
	Walk(v, c.From)
}

func (c *Cast) rewrite(r Rewriter) Node {
	c.From = Rewrite(r, c.From)
	return c
}

func (c *Cast) Encode(dst *ion.Buffer, st *ion.Symtab) {
	dst.BeginStruct(-1)
	settype(dst, st, "cast")
	dst.BeginField(st.Intern("from"))
	c.From.Encode(dst, st)
	dst.BeginField(st.Intern("to"))
	dst.WriteInt(int64(c.To))
	dst.EndStruct()
}

func (c *Cast) SetField(f ion.Field) error {
	switch f.Label {
	case "from":
		from, err := Decode(f.Datum)
		if err != nil {
			return err
		}
		c.From = from
	case "to":
		to, err := f.Int()
		if err != nil {
			return err
		}
		c.To = TypeSet(to)
	default:
		return errUnexpectedField
	}
	return nil
}

func (c *Cast) Equals(e Node) bool {
	ec, ok := e.(*Cast)
	if !ok {
		return false
	}
	return c.To == ec.To && c.From.Equals(ec.From)
}

type Timestamp struct {
	Value date.Time
}

func (t *Timestamp) UnixMicro() int64 {
	return t.Value.UnixMicro()
}

func (t *Timestamp) Type() TypeSet { return TimeType }

func (t *Timestamp) Trunc(part Timepart) Timestamp {
	year := t.Value.Year()
	month := t.Value.Month()
	day := t.Value.Day()
	hour := t.Value.Hour()
	minute := t.Value.Minute()
	second := t.Value.Second()
	nsec := t.Value.Nanosecond()

	// QUARTER truncates months the following way:
	//   - [1,2,3] to 1st,
	//   - [4,5,6] to 4th,
	//   - [7,8,9] to 7th,
	//   - [10,11,12] to 10th
	if part == Quarter {
		month = ((month-1)/3)*3 + 1
	}

	switch part {
	case Year:
		month = 1
		fallthrough
	case Quarter:
		// already truncated...
		fallthrough
	case Month:
		day = 1
		fallthrough
	case Day:
		hour = 0
		fallthrough
	case Hour:
		minute = 0
		fallthrough
	case Minute:
		second = 0
		fallthrough
	case Second:
		nsec = 0
	case Millisecond:
		nsec = (nsec / 1000000) * 1000000
	case Microsecond:
		nsec = (nsec / 1000) * 1000
	}

	return Timestamp{Value: date.Date(year, month, day, hour, minute, second, nsec)}
}

func (t *Timestamp) Datum() ion.Datum {
	return ion.Timestamp(t.Value)
}

func (t *Timestamp) text(dst *strings.Builder, redact bool) {
	var buf [48]byte
	dst.WriteByte('`')
	dst.Write(t.Value.AppendRFC3339Nano(buf[:0]))
	dst.WriteByte('`')
}

func (t *Timestamp) walk(v Visitor) {}

func (t *Timestamp) Encode(dst *ion.Buffer, st *ion.Symtab) {
	dst.WriteTime(t.Value)
}

func (t *Timestamp) Equals(e Node) bool {
	te, ok := e.(*Timestamp)
	return ok && t.Value.Equal(te.Value)
}

func (t *Timestamp) check(h Hint) error {
	y := t.Value.Year()
	// year >= 0 is an ion restriction;
	// year < (1 << 14)-1 is a restriction
	// to guarantee a 2-byte year component
	if y < 0 || y > (1<<14)-1 {
		return fmt.Errorf("timestamp %s out of serializeable range", t.Value)
	}
	return nil
}

// Weekday identifies a day of week starting from Sunday (0) to Saturday (6)
type Weekday int

const (
	Sunday Weekday = iota
	Monday
	Tuesday
	Wednesday
	Thursday
	Friday
	Saturday
)

var weekdayString = []string{
	Sunday:    "SUNDAY",
	Monday:    "MONDAY",
	Tuesday:   "TUESDAY",
	Wednesday: "WEDNESDAY",
	Thursday:  "THURSDAY",
	Friday:    "FRIDAY",
	Saturday:  "SATURDAY",
}

func (w Weekday) String() string {
	if w >= 0 && int(w) < len(weekdayString) {
		return weekdayString[w]
	}
	return "UNKNOWN"
}

// Timepart is an identifier
// that references part of a timestamp
type Timepart int

const (
	Microsecond Timepart = iota
	Millisecond
	Second
	Minute
	Hour
	Day
	DOW
	DOY
	Week
	Month
	Quarter
	Year
)

// time part -> string LUT
var partstring = []string{
	Microsecond: "MICROSECOND",
	Millisecond: "MILLISECOND",
	Second:      "SECOND",
	Minute:      "MINUTE",
	Hour:        "HOUR",
	Day:         "DAY",
	DOW:         "DOW",
	DOY:         "DOY",
	Week:        "WEEK",
	Month:       "MONTH",
	Quarter:     "QUARTER",
	Year:        "YEAR",
}

func (t Timepart) String() string {
	if t >= 0 && int(t) < len(partstring) {
		return partstring[t]
	}
	return "UNKNOWN"
}

func DateAdd(part Timepart, value, date Node) Node {
	return CallByName("DATE_ADD_"+part.String(), value, date)
}

func DateDiff(part Timepart, timestamp1, timestamp2 Node) Node {
	return CallByName("DATE_DIFF_"+part.String(), timestamp1, timestamp2)
}

func DateExtract(part Timepart, from Node) Node {
	return CallByName("DATE_EXTRACT_"+part.String(), from)
}

func DateTrunc(part Timepart, from Node) Node {
	// A WEEK without a weekday is WEEK(SUNDAY)
	if part == Week {
		return DateTruncWeekday(from, Sunday)
	}
	return CallByName("DATE_TRUNC_"+part.String(), from)
}

func DateTruncWeekday(from Node, dow Weekday) Node {
	return Call(DateTruncDOW, from, Integer(dow))
}

// Field is a field in a Struct literal,
type Field struct {
	// Label is the label for the field
	Label string
	// Value is a value in a Struct literal
	Value Constant
}

// Struct is a literal struct constant
type Struct struct {
	Fields []Field
}

func (s *Struct) Equals(e Node) bool {
	s2, ok := e.(*Struct)
	if !ok {
		return false
	}
	if len(s.Fields) != len(s2.Fields) {
		return false
	}
	for i := range s.Fields {
		if s2.Fields[i].Label != s.Fields[i].Label {
			return false
		}
		if !s2.Fields[i].Value.Equals(s.Fields[i].Value) {
			return false
		}
	}
	return true
}

func (s *Struct) Datum() ion.Datum {
	out := make([]ion.Field, 0, len(s.Fields))
	for i := range s.Fields {
		out = append(out, ion.Field{
			Label: s.Fields[i].Label,
			Datum: s.Fields[i].Value.Datum(),
		})
	}
	return ion.NewStruct(nil, out).Datum()
}

func (s *Struct) Type() TypeSet { return StructType }

func (s *Struct) walk(v Visitor) {}

func (s *Struct) text(dst *strings.Builder, redact bool) {
	dst.WriteByte('{')
	for i := range s.Fields {
		if i != 0 {
			dst.WriteString(", ")
		}
		quote(dst, s.Fields[i].Label)
		dst.WriteString(": ")
		s.Fields[i].Value.text(dst, redact)
	}
	dst.WriteByte('}')
}

func (s *Struct) Encode(dst *ion.Buffer, st *ion.Symtab) {
	dst.BeginStruct(-1)
	settype(dst, st, "struct")
	dst.BeginField(st.Intern("value"))
	s.Datum().Encode(dst, st)
	dst.EndStruct()
}

func (s *Struct) SetField(f ion.Field) error {
	switch f.Label {
	case "value":
		// should just be a raw structure datum
		rv, ok := AsConstant(f.Datum)
		if !ok {
			return fmt.Errorf("cannot use %s as constant", f.Type())
		}
		sval, ok := rv.(*Struct)
		if !ok {
			return fmt.Errorf("got value of type %T", rv)
		}
		s.Fields = sval.Fields
	default:
		return errUnexpectedField
	}
	return nil
}

// FieldByName returns value for given field or nil if there's no such field
func (s *Struct) FieldByName(f string) Constant {
	for i := range s.Fields {
		if s.Fields[i].Label == f {
			return s.Fields[i].Value
		}
	}

	return nil
}

func (s *Struct) HasField(f string) bool {
	return s.FieldByName(f) != nil
}

// List is a literal list constant.
type List struct {
	Values []Constant
}

func (l *List) text(dst *strings.Builder, redact bool) {
	dst.WriteByte('[')
	for i := range l.Values {
		if i != 0 {
			dst.WriteString(", ")
		}
		l.Values[i].text(dst, redact)
	}
	dst.WriteByte(']')
}

// Index returns position of the first occurance of constant.
// Returns -1 if no element was found.
func (l *List) Index(c Constant) int {
	for i := range l.Values {
		if Equal(c, l.Values[i]) {
			return i
		}
	}
	return -1
}

func (l *List) Type() TypeSet { return ListType }

func (l *List) Datum() ion.Datum {
	out := make([]ion.Datum, len(l.Values))
	for i := range l.Values {
		out[i] = l.Values[i].Datum()
	}
	return ion.NewList(nil, out).Datum()
}

func (l *List) Equals(e Node) bool {
	l2, ok := e.(*List)
	if !ok {
		return false
	}
	if len(l.Values) != len(l2.Values) {
		return false
	}
	for i := range l.Values {
		if !l.Values[i].Equals(l2.Values[i]) {
			return false
		}
	}
	return true
}

func (l *List) walk(v Visitor) {}

func (l *List) Encode(dst *ion.Buffer, st *ion.Symtab) {
	dst.BeginStruct(-1)
	settype(dst, st, "list")
	dst.BeginField(st.Intern("value"))
	l.Datum().Encode(dst, st)
	dst.EndStruct()
}

func (l *List) SetField(f ion.Field) error {
	switch f.Label {
	case "value":
		cnst, ok := AsConstant(f.Datum)
		if !ok {
			return fmt.Errorf("got value of type %s", f.Type())
		}
		lst, ok := cnst.(*List)
		if !ok {
			return fmt.Errorf("cannot use %T as *expr.List", cnst)
		}
		l.Values = lst.Values
	default:
		return errUnexpectedField
	}
	return nil
}

// AsConstant converts a literal ion datum
// into a literal PartiQL expression constant.
func AsConstant(d ion.Datum) (Constant, bool) {
	switch d.Type() {
	case ion.FloatType:
		d, _ := d.Float()
		return Float(d), true
	case ion.IntType:
		d, _ := d.Int()
		return Integer(d), true
	case ion.UintType:
		d, _ := d.Uint()
		if i := int64(d); i >= 0 {
			return Integer(int64(d)), true
		}
		return (*Rational)(big.NewRat(0, 0).SetUint64(d)), true
	case ion.StringType, ion.SymbolType:
		d, _ := d.String()
		return String(d), true
	case ion.StructType:
		d, _ := d.Struct()
		fields := make([]Field, 0, d.Len())
		ok := true
		d.Each(func(f ion.Field) error {
			var val Constant
			val, ok = AsConstant(f.Datum)
			if !ok {
				return ion.Stop
			}
			fields = append(fields, Field{
				Label: f.Label,
				Value: val,
			})
			return nil
		})
		if !ok {
			return nil, false
		}
		return &Struct{Fields: fields}, true
	case ion.ListType:
		d, _ := d.List()
		values := make([]Constant, 0, d.Len())
		ok := true
		d.Each(func(d ion.Datum) error {
			var val Constant
			val, ok = AsConstant(d)
			if !ok {
				return ion.Stop
			}
			values = append(values, val)
			return nil
		})
		if !ok {
			return nil, false
		}
		return &List{Values: values}, true
	case ion.TimestampType:
		d, _ := d.Timestamp()
		return &Timestamp{Value: d}, true
	case ion.BoolType:
		d, _ := d.Bool()
		return Bool(d), true
	case ion.NullType:
		return Null{}, true
	default:
		// TODO: add blob, clob, bags, etc.
		return nil, false
	}
}

// Unpivot captures the UNPIVOT t AS v AT a statement
type Unpivot struct {
	TupleRef Node    // t
	As       *string // v
	At       *string // a
}

func (u *Unpivot) Equals(brhs Node) bool {
	rhs, ok := brhs.(*Unpivot)
	if !ok {
		return false
	}
	if !equalPointed(u.As, rhs.As) {
		return false
	}
	if !equalPointed(u.At, rhs.At) {
		return false
	}
	return Equivalent(u.TupleRef, rhs.TupleRef)
}

func (u *Unpivot) Encode(dst *ion.Buffer, st *ion.Symtab) {
	dst.BeginStruct(-1)
	settype(dst, st, "unpivot")
	dst.BeginField(st.Intern("TupleRef"))
	u.TupleRef.Encode(dst, st)
	if u.As != nil {
		dst.BeginField(st.Intern("As"))
		dst.WriteString(*u.As)
	}
	if u.At != nil {
		dst.BeginField(st.Intern("At"))
		dst.WriteString(*u.At)
	}
	dst.EndStruct()
}

func (u *Unpivot) SetField(f ion.Field) error {
	var err error
	switch f.Label {
	case "As":
		var s string
		s, err = f.String()
		u.As = &s
	case "At":
		var s string
		s, err = f.String()
		u.At = &s
	case "TupleRef":
		u.TupleRef, err = Decode(f.Datum)
	default:
		return errUnexpectedField
	}
	return err
}

func (u *Unpivot) walk(v Visitor) {
	Walk(v, u.TupleRef)
}

func (u *Unpivot) text(dst *strings.Builder, redact bool) {
	dst.WriteString("UNPIVOT ")
	u.TupleRef.text(dst, redact)
	if u.As != nil {
		dst.WriteString(" AS ")
		dst.WriteString(*u.As)
	}
	if u.At != nil {
		dst.WriteString(" AT ")
		dst.WriteString(*u.At)
	}
}

func equalPointed[T comparable](lhs, rhs *T) bool {
	if lhs != nil {
		return (rhs != nil) && ((lhs == rhs) || (*lhs == *rhs))
	}
	return rhs == nil
}

// ExplainFormat describes the format of explain output
type ExplainFormat uint8

const (
	// ExplainNone does no explain
	ExplainNone ExplainFormat = iota

	// ExplainDefault returns singe blob of text
	ExplainDefault

	// ExplainText returns a singe blob of text
	ExplainText

	// ExplainList returns each line of plan separately
	ExplainList

	// ExplainGraphviz returns plan in graphviz format
	ExplainGraphviz
)

// UnionType describes type of union expression
type UnionType uint8

const (
	// UNION without duplicates
	UnionDistinct UnionType = iota

	// UNION with duplicates
	UnionAll
)

func (t UnionType) String() string {
	switch t {
	case UnionDistinct:
		return "UNION"
	case UnionAll:
		return "UNION ALL"
	default:
		return fmt.Sprintf("<UnionType=%d>", int(t))
	}
}

// Union describes a single pair of expressions connected by UNION keyword
type Union struct {
	Type  UnionType
	Left  Node
	Right Node
}

func (u *Union) Equals(n Node) bool {
	u2, ok := n.(*Union)
	if !ok {
		return false
	}

	return u.Type == u2.Type &&
		u.Left.Equals(u2.Left) &&
		u.Right.Equals(u2.Right)
}

func (u *Union) Encode(dst *ion.Buffer, st *ion.Symtab) {
	dst.BeginStruct(-1)
	settype(dst, st, "union")
	dst.BeginField(st.Intern("uniontype"))
	dst.WriteUint(uint64(u.Type))
	dst.BeginField(st.Intern("left"))
	u.Left.Encode(dst, st)
	dst.BeginField(st.Intern("right"))
	u.Right.Encode(dst, st)
	dst.EndStruct()
}

func (u *Union) SetField(f ion.Field) error {
	switch f.Label {
	case "uniontype":
		v, err := f.Uint()
		if err != nil {
			return err
		}
		u.Type = UnionType(v)
	case "left":
		v, err := Decode(f.Datum)
		if err != nil {
			return err
		}
		u.Left = v
	case "right":
		v, err := Decode(f.Datum)
		if err != nil {
			return err
		}
		u.Right = v
	default:
		return errUnexpectedField
	}
	return nil
}

func (u *Union) walk(v Visitor) {
	Walk(v, u.Left)
	Walk(v, u.Right)
}

func (u *Union) text(dst *strings.Builder, redact bool) {
	write := func(n Node) {
		if n == nil {
			dst.WriteString("<nil>")
			return
		}

		if sel, ok := n.(*Select); ok {
			sel.write(dst, redact, nil)
		} else {
			n.text(dst, redact)
		}
	}

	write(u.Left)
	fmt.Fprintf(dst, " %s ", u.Type)
	write(u.Right)
}

var _ Node = &Union{}

func asrational(e Node) *big.Rat {
	n, ok := e.(number)
	if !ok {
		return nil
	}

	return n.rat()
}
