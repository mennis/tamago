// VirtIO entropy device driver
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

// Package rng implements a driver for VirtIO entropy devices (virtio-rng)
// following reference specifications:
//   - Virtual I/O Device (VIRTIO) - Version 1.2, 5.4 Entropy Device
//
// This package is only meant to be used with `GOOS=tamago` as
// supported by the TamaGo framework for bare metal Go, see
// https://github.com/usbarmory/tamago.
package rng

import (
	"errors"
	"sync"

	"github.com/usbarmory/tamago/kvm/virtio"
)

// VirtIO entropy device ID
const DeviceID = 4

const (
	// queue size, bounded by the device maximum
	queueSize = 8
	// device-writable buffer length
	bufferSize = 64
	// polls of the used ring before giving up on a request
	pollLimit = 1 << 20
)

// RNG represents a VirtIO entropy device instance.
type RNG struct {
	sync.Mutex

	// IO represents the VirtIO transport instance
	IO virtio.VirtIO

	queue *virtio.VirtualQueue
	buf   []byte
}

// Init initializes the VirtIO entropy device.
func (hw *RNG) Init() (err error) {
	if hw.IO == nil {
		return errors.New("invalid VirtIO instance")
	}

	if err = hw.IO.Init(0); err != nil {
		return
	}

	if id := hw.IO.DeviceID(); id != DeviceID {
		return errors.New("not an entropy device")
	}

	size := min(hw.IO.MaxQueueSize(0), queueSize)

	if size == 0 {
		return errors.New("queue unavailable")
	}

	hw.queue = &virtio.VirtualQueue{}
	hw.queue.Init(size, bufferSize, virtio.Write)
	hw.buf = make([]byte, bufferSize)

	hw.IO.SetQueueSize(0, size)
	hw.IO.SetQueue(0, hw.queue)
	hw.IO.SetReady()
	hw.IO.QueueNotify(0)

	return
}

// Read fills b with entropy from the device, it returns an error if the
// device stops supplying it.
func (hw *RNG) Read(b []byte) (n int, err error) {
	hw.Lock()
	defer hw.Unlock()

	if hw.queue == nil {
		return 0, errors.New("device not initialized")
	}

	for n < len(b) {
		var m int

		for i := 0; m == 0 && i < pollLimit; i++ {
			if m, err = hw.queue.Pop(hw.buf); err != nil {
				return
			}
		}

		if m == 0 {
			return n, errors.New("device timeout")
		}

		n += copy(b[n:], hw.buf[:m])

		// return the buffer to the device
		hw.IO.QueueNotify(0)
	}

	return
}
