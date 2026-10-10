// QEMU virt (arm64) support for tamago/arm64
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package virt

import (
	"encoding/binary"
	_ "unsafe"

	"github.com/usbarmory/tamago/internal/rng"
)

// WARNING: the machine exposes no hardware entropy source, the DRBG is seeded
// only from the generic timer counter and is not suitable for cryptographic use.
var drbg = &rng.DRBG{}

//go:linkname initRNG runtime/goos.InitRNG
func initRNG() {
	for i := 0; i < 4; i++ {
		c := ARM.Counter()
		binary.LittleEndian.PutUint64(drbg.Seed[i*8:], c*0x9e3779b97f4a7c15+uint64(i))
	}

	rng.GetRandomDataFn = drbg.GetRandomData
}
