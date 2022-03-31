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

// Code generated by ragel. DO NOT EDIT.

package jsonrl

import (
       "fmt"
       "errors"
       "unicode/utf8"
)

%%{
    machine datum;

    # one-character escape sequence:
    escape_sequence = (("\\" [tvfnrab\\\"/]) | ("\\u" xdigit{4})) > {esc = true};
    # multi-byte unicode sequences
    # (must be printable)
    # FIXME: perform rune calculation in-line; it is faster
    unicode_2c = (192 .. 223) . (128 .. 191) %{{
        r, size := utf8.DecodeRune(data[p-2:])
        if size != 2 {
           return p-2, fmt.Errorf("bad rune %x", r)
        }
    }};
    unicode_3c = (224 .. 239) . (128 .. 191) . (128 .. 191) %{{
        r, size := utf8.DecodeRune(data[p-3:])
        if size != 3 {
            return p-3, fmt.Errorf("bad rune %x", r)
        }
    }};
    unicode_4c = (240 .. 247) . (128 .. 191) . (128 .. 191) . (128 .. 191) %{{
        r, size := utf8.DecodeRune(data[p-4:])
        if size != 4 {
           return p-4, fmt.Errorf("bad rune %x", r)
        }
    }};

    unicode_sequence = unicode_2c | unicode_3c | unicode_4c;

    # one string character: printable or escape sequence
    string_chars = (ascii - [\\\"]) | escape_sequence | unicode_sequence;

    # quoted string: zero or more string charaters
    # (we capture the start and end offsets of the string)
    qstring = '"' %from{esc = false; sbegin = p;} (string_chars*) '"' >from{send = p};

    unsigned = digit+ ${curi *= 10; curi += uint64(data[p] - '0');};
    jsint = (('-' @{neg = true})? unsigned) >{neg = false;} %{{
          i := int64(curi)
          /* FIXME: what if this integer is out of range? */
          if neg {
             i = -i
          }
          s.parseInt(i); curi = 0;
    }};

    # decimal part: continue parsing mantissa; track exponent
    decpart = digit* ${curi *= 10; curi += uint64(data[p] - '0'); dc--};
    # exponent part: parse exponent integer; add to decimal part
    epart = ((('-' @{nege = true})|'+')?) digit* ${cure *= 10; cure += int(data[p]) - '0';} %{
          if nege {
             cure = -cure
          }
          dc += cure
    };
    # fixme: if the input float string is long enough
    # that it could have overflowed the mantissa or exponent,
    # we should fall back to parsing the text using more precision
    # (for example, parsing "1.00000000000000011102230246251565404236316680908203126")
    jsdec = (('-' @{neg = true})? unsigned) '.' decpart ([eE] epart)? %{
          atof(s, curi, dc, neg)
          curi = 0; dc = 0; cure = 0; neg = false; nege = false;
    };

    jsnull = "null" %{ s.parseNull() };
    jstrue = "true" %{ s.parseBool(true) };
    jsfalse = "false" %{ s.parseBool(false); };
    jsrec = '{' @{{
          p++ // skip '{'
          npe, err := parseRecord(s, data[p:])
          p += npe // skip struct body
          if err != nil {
             return p, err
          }
          // will automatically advance past '}'
    }};
    jslist = '[' @{{
           p++ // skip '['
           npe, err := parseList(s, data[p:])
           p += npe
           if err != nil {
              return p, err
           }
           // will automatically advance past ']'
    }};
    jsstr = qstring %from{ s.parseString(data[sbegin:send], esc) };
    object = space* (jsnull | jstrue | jsfalse | jsdec | jsint | jsstr | jsrec | jslist);
}%%

%%{
    machine object;
    include datum;
    main := object @{{
         // since this is the 'final' state,
         // advance the character pointer
         // so that it points past the final char
         // (i.e. return the # of characters consumed)
         return p+1, nil
    }};
}%%

%% write data nofinal;

var ErrNoMatch = errors.New("jsonrl: bad object text")

func ParseObject(s *State, data []byte) (int, error) {
     neg, nege, esc := false, false, false
     sbegin, send := 0, 0
     curi, cure, dc := uint64(0), int(0), int(0)
     cs, p, pe, eof := 0, 0, len(data), len(data)
     _ = eof
     %%{
        write init;
        write exec;
     }%%
     return p, fmt.Errorf("ParseObject: position %d of %d: %w", p, pe, ErrNoMatch)
}

%%{
    machine recfields;
    include datum;
    label = qstring %from{s.beginField(data[sbegin:send], esc)};
    field = label space* ":" space* object;
    main := (space* (field (',' space* field)*)? space* '}') @{
         s.endRecord()
         return p, nil
    };
}%%

%% write data nofinal;

func parseRecord(s *State, data []byte) (int, error) {
     neg, nege, esc := false, false, false
     sbegin, send := 0, 0
     curi, cure, dc := uint64(0), int(0), int(0)
     s.beginRecord()
     cs, p, pe, eof := 0, 0, len(data), len(data)
     _ = eof
     %%{
        write init;
        write exec;
     }%%
     return p, fmt.Errorf("parseRecord: position %d of %d: %w", p, pe, ErrNoMatch)
}

%%{
    machine listfields;
    include datum;
    main := space* (object space* (',' space* object)*)? space* ']' @{
         s.endList()
         return p, nil
    };
}%%

%% write data;

func parseList(s *State, data []byte) (int, error) {
     neg, nege, esc := false, false, false
     sbegin, send := 0, 0
     curi, cure, dc := uint64(0), int(0), int(0)
     s.beginList()
     cs, p, pe, eof := 0, 0, len(data), len(data)
     _ = eof
     %%{
        write init;
        write exec;
     }%%
     return p, ErrNoMatch
}
