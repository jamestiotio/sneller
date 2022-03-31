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

package partiql

import (
	"fmt"
	"unicode/utf8"
)

func appendRune(dst []byte, r rune) []byte {
	// FIXME: this is slow and gross
	tmp := append(dst, 0, 0, 0, 0)
	l := utf8.EncodeRune(tmp[len(dst):], r)
	return tmp[:len(dst)+l]
}

// unescaped
func unescape(buf []byte) (string, error) {
	var tmp []byte
	for i := 0; i < len(buf); i++ {
		c := buf[i]
		if c >= utf8.RuneSelf {
			r, size := utf8.DecodeRune(buf[i:])
			if r == utf8.RuneError {
				return "", fmt.Errorf("invalid rune %x", buf[i:i+size])
			} else {
				tmp = append(tmp, buf[i:i+size]...)
			}
			i += size - 1
			continue
		} else if c != '\\' {
			tmp = append(tmp, c)
			continue
		}
		i++
		if i >= len(buf) {
			return "", fmt.Errorf("cannot unescape trailing \\")
		}
		c = buf[i]
		// from lex.rl:
		// escape_sequence = (("\\" [tvfnrab\\'/]) | ("\\u" xdigit{4}))
		switch c {
		case '\\':
			tmp = append(tmp, '\\')
		case 't':
			tmp = append(tmp, '\t')
		case 'n':
			tmp = append(tmp, '\n')
		case 'r':
			tmp = append(tmp, '\r')
		case 'v':
			tmp = append(tmp, '\v')
		case 'f':
			tmp = append(tmp, '\f')
		case 'a':
			tmp = append(tmp, '\a')
		case 'b':
			tmp = append(tmp, '\b')
		case '\'':
			tmp = append(tmp, '\'')
		case '/':
			tmp = append(tmp, '/')
		case 'u':
			r := rune(0)
			i++
			for j := i; j < i+4; j++ {
				if j >= len(buf) {
					return "", fmt.Errorf("invalid \\u escape sequence")
				}
				add := rune(buf[j])
				if add >= '0' && add <= '9' {
					add -= '0'
				} else if add >= 'A' && add <= 'F' {
					add -= 'A'
					add += 10
				} else if add >= 'a' && add <= 'f' {
					add -= 'a'
					add += 10
				}
				r = (r * 16) + add
			}
			i += 3
			if !utf8.ValidRune(r) {
				return "", fmt.Errorf("rune U%x is invalid", r)
			}
			tmp = appendRune(tmp, r)
		default:
			return "", fmt.Errorf("unexpected backslash escape of %c", c)
		}
	}
	return string(tmp), nil
}
