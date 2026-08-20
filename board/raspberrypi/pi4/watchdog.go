// Raspberry Pi 4 watchdog timer support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package pi4

import (
	"github.com/usbarmory/tamago/soc/bcm2711"
)

// Watchdog provides access to the BCM2711 hardware watchdog timer, typically
// used to reset the board on a lock-up such as an out-of-memory halt.
//
// Start it with a timeout and call Reset within that timeout:
//
//	pi4.Watchdog.Start(10 * time.Second)
//	for {
//	    // do work
//	    pi4.Watchdog.Reset()
//	}
//
// This is the Broadcom PM block watchdog, not an SP805: different registers,
// different unlock password, and a tick that caps the timeout at roughly 16.7
// seconds. Start refuses a timeout that does not fit rather than saturating.
var Watchdog = bcm2711.Watchdog
