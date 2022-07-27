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

package zion

import (
	"encoding/binary"

	"github.com/SnellerInc/sneller/ion"

	"github.com/dchest/siphash"
)

const (
	bucketBits = 4
	buckets    = 1 << bucketBits
	bucketMask = buckets - 1
)

func sym2bucket(seed uint64, sym ion.Symbol) int {
	var buf [9]byte
	size := binary.PutUvarint(buf[:], uint64(sym))
	return int(siphash.Hash(0, seed, buf[:size]) & bucketMask)
}