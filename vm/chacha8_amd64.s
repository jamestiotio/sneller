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

#include "go_asm.h"
#include "textflag.h"
#include "funcdata.h"

// chacha8 random initialization vector
GLOBL chachaiv<>(SB), 8, $64
DATA  chachaiv<>+0(SB)/4, $0x9722F977  // XOR'd with length for real IV
DATA  chachaiv<>+4(SB)/4, $0x3320646e
DATA  chachaiv<>+8(SB)/4, $0x79622d32
DATA  chachaiv<>+12(SB)/4, $0x6b206574
DATA  chachaiv<>+16(SB)/4, $0x058A60F5
DATA  chachaiv<>+20(SB)/4, $0xB25F6FB1
DATA  chachaiv<>+24(SB)/4, $0x1FEFA3D9
DATA  chachaiv<>+28(SB)/4, $0xB9D8F520
DATA  chachaiv<>+32(SB)/4, $0xB415DBCC
DATA  chachaiv<>+36(SB)/4, $0x34B70366
DATA  chachaiv<>+40(SB)/4, $0x3F4DBB4D
DATA  chachaiv<>+44(SB)/4, $0xCBB67392
DATA  chachaiv<>+48(SB)/4, $0x61707865
DATA  chachaiv<>+52(SB)/4, $0x143BE9F6
DATA  chachaiv<>+56(SB)/4, $0xDA97A1A8
DATA  chachaiv<>+60(SB)/4, $0x6F0E9495

TEXT ·chacha8x4(SB), NOSPLIT, $0
  MOVQ      base+0(FP), R15
  VMOVDQU   ends+8(FP), X11 // X11 = end positions
  VPSLLDQ   $4, X11, X10    // X10 = offsets = lengths[lane-1]
  VPSUBD    X10, X11, X11   // X11 = lengths (end position - offset[lane-1])
  VBROADCASTI32X4 chachaiv<>+00(SB), Z9
  CALL      hashx4(SB)
  VMOVDQU32 Z9, ret+24(FP)
  RET
