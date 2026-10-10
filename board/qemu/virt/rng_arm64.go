// QEMU virt (arm64) support for tamago/arm64
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package virt

import (
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"io"
	_ "unsafe"

	"github.com/usbarmory/tamago/internal/reg"
	"github.com/usbarmory/tamago/internal/rng"
	"github.com/usbarmory/tamago/kvm/virtio"
	virtiorng "github.com/usbarmory/tamago/kvm/virtio/rng"
)

// WARNING: unless InitEntropy finds a VirtIO entropy device the DRBG is seeded
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

// VirtIO MMIO transports
const (
	VIRTIO_MMIO_BASE  = 0x0a000000
	VIRTIO_MMIO_SIZE  = 0x200
	VIRTIO_MMIO_SLOTS = 32
)

// RNG is the VirtIO entropy device, nil until InitEntropy finds one.
var RNG *virtiorng.RNG

// InitEntropy probes the VirtIO MMIO slots for an entropy device and, when
// found, rekeys the DRBG from it. Randomness drawn before the call comes from
// the counter-seeded DRBG.
//
// Every slot is read, so the call is only safe on a machine that answers the
// whole VirtIO MMIO window (QEMU does), it is therefore not made by default.
func InitEntropy() error {
	for i := range VIRTIO_MMIO_SLOTS {
		base := uint32(VIRTIO_MMIO_BASE + i*VIRTIO_MMIO_SIZE)

		if reg.Read(base+virtio.Magic) != virtio.MAGIC || reg.Read(base+virtio.DeviceID) != virtiorng.DeviceID {
			continue
		}

		dev := &virtiorng.RNG{IO: &virtio.MMIO{Base: base}}

		if err := dev.Init(); err != nil {
			return err
		}

		var seed [32]byte

		if _, err := io.ReadFull(dev, seed[:]); err != nil {
			return err
		}

		drbg.Lock()
		subtle.XORBytes(drbg.Seed[:], drbg.Seed[:], seed[:])
		drbg.Unlock()

		RNG = dev

		return nil
	}

	return errors.New("no VirtIO entropy device")
}
