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

// Code generated by Ragel. DO NOT EDIT.

package elastic_proxy

import (
    "fmt"
    "strconv"
    "time"
)

%%{
    machine datemathlexer;

    write data;
    access lex.;
    variable p lex.p;
    variable pe lex.pe;
}%%

type dateMathLexer struct {
    data []byte
    p, pe, cs int
    ts int

    now time.Time
    t time.Time
    number int
}

func (l dateMathLexer) string() string {
    return string(l.data[l.ts:l.p])
}

func ParseDateMath(data string, now time.Time) (time.Time, error) {
    lex := &dateMathLexer{
        data: []byte(data),
        pe:   len(data),
        now:  now,
    }

    eof := len(data)
    var result time.Time
    %%{
        action mark       { lex.ts = lex.p; }
        action setTimeNow { lex.t = lex.now; }
        action setDate    { lex.t = parseDate(lex.string()); }
        action addTime    { lex.t = addTime(lex.t, lex.string()); }
        action setNumber  { lex.number, _ = strconv.Atoi(lex.string()); }
        action adjust     { lex.t = adjust(lex.t, lex.number, lex.string()); }
        action round      { lex.t = round(lex.t, lex.string()); }

        dateNow = "now" %setTimeNow;
        datePart = (digit{4} [.\-] digit{1,2} [.\-] digit{1,2}) >mark %setDate;
        timePart = (digit{1,2} ":" digit{1,2} (":" digit{1,2} ("." digit{1,9})?)?) >mark %addTime;
        dateSpec = datePart ([T ] timePart)?;
        date = dateNow | (dateSpec "||");
        adjust = [+\-] >mark digit+ %setNumber [yMwdhHms] >mark %adjust;
        round = '/' [yMwdhHms] >mark %round;

        main := (date (round | adjust)*) %{ result = lex.t; };
    }%%

    %% write init;
    %% write exec;

    if lex.p != eof {
        return result, fmt.Errorf("invalid format %q", string(lex.data))
    }
    return result, nil
}