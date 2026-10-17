#include "textflag.h"

// func dotAVX2(a, b *float32, n int) float32
TEXT ·dotAVX2(SB), NOSPLIT, $0-28
	MOVQ a+0(FP), SI
	MOVQ b+8(FP), DI
	MOVQ n+16(FP), CX
	VXORPS Y0, Y0, Y0
	VXORPS Y1, Y1, Y1

loop16:
	CMPQ CX, $16
	JL   loop8
	VMOVUPS (SI), Y2
	VMOVUPS 32(SI), Y3
	VFMADD231PS (DI), Y2, Y0
	VFMADD231PS 32(DI), Y3, Y1
	ADDQ $64, SI
	ADDQ $64, DI
	SUBQ $16, CX
	JMP  loop16

loop8:
	CMPQ CX, $8
	JL   reduce
	VMOVUPS (SI), Y2
	VFMADD231PS (DI), Y2, Y0
	ADDQ $32, SI
	ADDQ $32, DI
	SUBQ $8, CX

reduce:
	VADDPS       Y1, Y0, Y0
	VEXTRACTF128 $1, Y0, X1
	VADDPS       X1, X0, X0
	VHADDPS      X0, X0, X0
	VHADDPS      X0, X0, X0

tail:
	CMPQ CX, $0
	JE   done
	VMOVSS      (SI), X2
	VFMADD231SS (DI), X2, X0
	ADDQ $4, SI
	ADDQ $4, DI
	DECQ CX
	JMP  tail

done:
	VZEROUPPER
	MOVSS X0, ret+24(FP)
	RET
