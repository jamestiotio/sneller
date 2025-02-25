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
	"strings"

	"github.com/SnellerInc/sneller/expr"
	"github.com/SnellerInc/sneller/ion"
	"github.com/SnellerInc/sneller/vm"
)

// Project is a plan Op that
// projects and re-names fields
type Project struct {
	Nonterminal
	Using []expr.Binding
}

func (p *Project) rewrite(rw expr.Rewriter) {
	p.From.rewrite(rw)
	for i := range p.Using {
		p.Using[i].Expr = expr.Rewrite(rw, p.Using[i].Expr)
	}
}

func (p *Project) exec(dst vm.QuerySink, src TableHandle, ep *ExecParams) error {
	proj, err := vm.NewProjection(ep.rewriteBind(p.Using), dst)
	if err != nil {
		return err
	}
	return p.From.exec(proj, src, ep)
}

func (p *Project) encode(dst *ion.Buffer, st *ion.Symtab, rw expr.Rewriter) error {
	dst.BeginStruct(-1)
	settype("project", dst, st)
	dst.BeginField(st.Intern("project"))
	encodeBindings(p.Using, dst, st, rw)
	dst.EndStruct()
	return nil
}

func (p *Project) setfield(d Decoder, f ion.Field) error {
	switch f.Label {
	case "project":
		bind, err := expr.DecodeBindings(f.Datum)
		if err != nil {
			return err
		}
		p.Using = bind
	default:
		return errUnexpectedField
	}

	return nil
}

func (p *Project) String() string {
	var out strings.Builder
	out.WriteString("PROJECT ")
	for i := range p.Using {
		out.WriteString(expr.ToString(&p.Using[i]))
		if i != len(p.Using)-1 {
			out.WriteString(", ")
		}
	}
	return out.String()
}
