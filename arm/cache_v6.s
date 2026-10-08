// ARM processor support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

// ARMv5 and ARMv6 cores have no cache level hierarchy to walk, the whole data
// cache is cleaned and invalidated with a single operation.

//go:build !arm.7

#include "textflag.h"

// func cache_flush_data()
TEXT ·cache_flush_data(SB),$0
	MOVW	$0, R0

	// clean and invalidate entire data cache
	MCR	15, 0, R0, C7, C14, 0

	// data synchronization barrier
	MCR	15, 0, R0, C7, C10, 4

	RET
