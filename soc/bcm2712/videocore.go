// BCM2712 SoC VideoCore support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package bcm2712

import "encoding/binary"

const (
	GPU_MEMORY_FLAG_DISCARDABLE      = 1 << 0
	GPU_MEMORY_FLAG_NORMAL           = 0 << 2
	GPU_MEMORY_FLAG_DIRECT           = 1 << 2
	GPU_MEMORY_FLAG_COHERENT         = 2 << 2
	GPU_MEMORY_FLAG_L1_NONALLOCATING = 3 << 2
	GPU_MEMORY_FLAG_ZERO             = 1 << 4
	GPU_MEMORY_FLAG_NO_INIT          = 1 << 5
	GPU_MEMORY_FLAG_HINT_PERMALOCK   = 1 << 6
)

// FirmwareRevision gets the firmware revision of the VideoCore GPU
// (VC_FIRMWARE_GET_REV). soc/bcm2835's accessor of the same name sends
// VC_BOARD_GET_REV instead, returning the board revision.
func FirmwareRevision() uint32 {
	buf := exchangeSingleTagMessage(VC_FIRMWARE_GET_REV, make([]byte, VC_FIRMWARE_GET_REV_LEN))

	if len(buf) < 4 {
		return 0
	}

	return binary.LittleEndian.Uint32(buf)
}

// BoardRevision gets the board revision code (VC_BOARD_GET_REV).
//
// New-style codes (bit 23 set) encode the total RAM in bits [22:20]: 0=256MB
// 1=512MB 2=1GB 3=2GB 4=4GB 5=8GB 6=16GB. Unlike [CPUMemory] this covers the
// models above 4GB.
func BoardRevision() uint32 {
	buf := exchangeSingleTagMessage(VC_BOARD_GET_REV, make([]byte, VC_BOARD_GET_REV_LEN))

	if len(buf) < 4 {
		return 0
	}

	return binary.LittleEndian.Uint32(buf)
}

// BoardModel gets the board model
func BoardModel() uint32 {
	buf := exchangeSingleTagMessage(VC_BOARD_GET_MODEL, make([]byte, VC_BOARD_GET_MODEL_LEN))

	if len(buf) < 4 {
		return 0
	}

	return binary.LittleEndian.Uint32(buf)
}

// MACAddress gets the board's MAC address
func MACAddress() []byte {
	return exchangeSingleTagMessage(VC_BOARD_GET_MAC, make([]byte, VC_BOARD_GET_MAC_LEN))
}

// Serial gets the low 32 bits of the board's serial number, the default
// network boot TFTP directory prefix. Use [Serial64] for the full value.
func Serial() uint32 {
	buf := exchangeSingleTagMessage(VC_BOARD_GET_SERIAL, make([]byte, VC_BOARD_GET_SERIAL_LEN))

	if len(buf) < 4 {
		return 0
	}

	return binary.LittleEndian.Uint32(buf)
}

// Serial64 gets the board's full 64-bit serial number, as reported by Linux
// in /proc/cpuinfo.
func Serial64() uint64 {
	buf := exchangeSingleTagMessage(VC_BOARD_GET_SERIAL, make([]byte, VC_BOARD_GET_SERIAL_LEN))

	if len(buf) < 8 {
		return uint64(Serial())
	}

	return binary.LittleEndian.Uint64(buf)
}

// CPUMemory gets the memory ranges allocated to the ARM core(s)
func CPUMemory() (start uint32, size uint32) {
	buf := exchangeSingleTagMessage(VC_BOARD_GET_ARM_MEMORY, make([]byte, VC_BOARD_GET_ARM_MEMORY_LEN))

	if len(buf) < 8 {
		return 0, 0
	}

	return binary.LittleEndian.Uint32(buf[0:]), binary.LittleEndian.Uint32(buf[4:])
}

// GPUMemory gets the memory ranges allocated to VideoCore
func GPUMemory() (start uint32, size uint32) {
	buf := exchangeSingleTagMessage(VC_BOARD_GET_VC_MEMORY, make([]byte, VC_BOARD_GET_VC_MEMORY_LEN))

	if len(buf) < 8 {
		return 0, 0
	}

	return binary.LittleEndian.Uint32(buf[0:]), binary.LittleEndian.Uint32(buf[4:])
}

// CPUAvailableDMAChannels gets the DMA channels available to the ARM core(s)
func CPUAvailableDMAChannels() (bitmask uint32) {
	buf := exchangeSingleTagMessage(VC_RES_GET_DMACHANNELS, make([]byte, VC_RES_GET_DMACHANNELS_LEN))

	if len(buf) < 4 {
		return 0
	}

	return binary.LittleEndian.Uint32(buf)
}

// AllocateGPUMemory allocates space from the GPU address space
//
// The returned value is a handle, use LockMemory to convert
// to an address.
func AllocateGPUMemory(size uint32, alignment uint32, flags uint32) (handle uint32) {
	buf := make([]byte, VC_MEM_ALLOCATE_LEN)
	binary.LittleEndian.PutUint32(buf[0:], size)
	binary.LittleEndian.PutUint32(buf[4:], alignment)
	binary.LittleEndian.PutUint32(buf[8:], uint32(flags))

	buf = exchangeSingleTagMessage(VC_MEM_ALLOCATE, buf)

	if len(buf) < 4 {
		return 0
	}

	return binary.LittleEndian.Uint32(buf)
}

// LockGPUMemory provides the address of previously allocated memory
func LockGPUMemory(handle uint32) (addr uint32) {
	buf := make([]byte, VC_MEM_LOCK_LEN)
	binary.LittleEndian.PutUint32(buf[0:], handle)

	buf = exchangeSingleTagMessage(VC_MEM_LOCK, buf)

	if len(buf) < 4 {
		return 0
	}

	return binary.LittleEndian.Uint32(buf)
}

func exchangeSingleTagMessage(code uint32, buf []byte) []byte {
	msg := &MailboxMessage{
		Tags: []MailboxTag{
			{
				ID:     code,
				Buffer: buf,
			},
		},
	}

	Mailbox.Call(VC_CH_PROPERTYTAGS_A_TO_VC, msg)

	tag := msg.Tag(code)

	if tag == nil {
		return nil
	}

	return tag.Buffer
}
