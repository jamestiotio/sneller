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
	"testing"
)

func TestStackMapBasics(t *testing.T) {
	var m stackmap
	n := uint(0)

	assertStackSlot := func(got, expected stackslot) {
		if got != expected {
			t.Helper()
			t.Errorf("expected slot to be %d, not %d", uint(expected), uint(got))
		}
		n += 1
	}

	kSize := regK.size() // Assumed to be 2 here...
	vSize := regV.size()
	hSize := regH.size()

	// StackMap should allocate slots from 0.
	assertStackSlot(m.allocSlot(regK), stackslot(0))

	// When the size of the register is less than 8 (our allocation unit)
	// it should keep the internal offset aligned so registers that need
	// more than 8 bytes are aligned to 8 bytes.
	assertStackSlot(m.allocSlot(regV), stackslot(bcStackAlignment/2))
	assertStackSlot(m.allocSlot(regV), stackslot(bcStackAlignment/2+vRegSize))

	// StackMap should use the space required for alignment to allocation
	// unit for the same register group.
	assertStackSlot(m.allocSlot(regK), stackslot(kSize))
	assertStackSlot(m.allocSlot(regK), stackslot(kSize*2))
	assertStackSlot(m.allocSlot(regK), stackslot(kSize*3))
	assertStackSlot(m.allocSlot(regK), stackslot(kSize*4))
	assertStackSlot(m.allocSlot(regK), stackslot(kSize*5))
	assertStackSlot(m.allocSlot(regK), stackslot(kSize*6))

	// Hash registers use separate slots at the moment.
	assertStackSlot(m.allocSlot(regH), stackslot(bcStackAlignment+vRegSize*2))
	assertStackSlot(m.allocSlot(regH), stackslot(bcStackAlignment+vRegSize*2+hSize))

	vStackSize := m.stackSize()
	expectedSize := int(vSize*2 + bcStackAlignment + hSize*2)
	if vStackSize != expectedSize {
		t.Errorf("invalid virtual stack size reported: expected %d, got %d", expectedSize, vStackSize)
	}

	// Properly aligned stack size should be reported even if the last register is K.
	assertStackSlot(m.allocSlot(regK), stackslot(kSize*7))

	vStackSize = m.stackSize()
	if vStackSize != expectedSize {
		t.Errorf("invalid virtual stack size when checking alignment: expected %d, got %d", expectedSize, vStackSize)
	}
}

func TestStackMapWithReservedSlotsAtTheBeginning(t *testing.T) {
	var m stackmap
	n := uint(0)

	assertStackSlot := func(got, expected stackslot) {
		if got != expected {
			t.Errorf("expected slot[%d] to be %d, not %d", n, uint(expected), uint(got))
		}
		n += 1
	}

	vSize := regV.size()

	// Reserve some slots that cannot be allocated.
	m.reserveSlot(regV, stackslot(vSize*0))
	m.reserveSlot(regV, stackslot(vSize*1))
	m.reserveSlot(regV, stackslot(vSize*2))
	m.reserveSlot(regV, stackslot(vSize*3))

	// The first allocated slot should be at vSize*4 as everything up to that is reserved.
	assertStackSlot(m.allocSlot(regV), stackslot(vSize*4))
	assertStackSlot(m.allocSlot(regV), stackslot(vSize*5))
	assertStackSlot(m.allocSlot(regV), stackslot(vSize*6))
}

func TestStackMapWithReservedSlotInTheMiddle(t *testing.T) {
	var m stackmap
	n := uint(0)

	assertStackSlot := func(got, expected stackslot) {
		if got != expected {
			t.Helper()
			t.Errorf("expected slot[%d] to be %d, not %d", n, uint(expected), uint(got))
		}
		n += 1
	}

	vSize := regV.size()

	// Reserve some slots first.
	m.reserveSlot(regV, stackslot(vSize))
	m.reserveSlot(regV, stackslot(vSize*4))
	start := stackslot(vSize * 5)

	// The first allocated slot should be at the beginning, because there is enough space.
	assertStackSlot(m.allocSlot(regV), start+stackslot(0))

	// The second and third slot should start from vSize*2 (as vSize bytes at vSize were reserved).
	assertStackSlot(m.allocSlot(regV), start+stackslot(vSize*1))
	assertStackSlot(m.allocSlot(regV), start+stackslot(vSize*2))

	// All other slots should continue from vSize*4.
	assertStackSlot(m.allocSlot(regV), start+stackslot(vSize*3))
	assertStackSlot(m.allocSlot(regV), start+stackslot(vSize*4))
	assertStackSlot(m.allocSlot(regV), start+stackslot(vSize*5))
	assertStackSlot(m.allocSlot(regV), start+stackslot(vSize*6))
	assertStackSlot(m.allocSlot(regV), start+stackslot(vSize*7))
	assertStackSlot(m.allocSlot(regV), start+stackslot(vSize*8))
	assertStackSlot(m.allocSlot(regV), start+stackslot(vSize*9))
}
