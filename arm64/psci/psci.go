// ARM Power State Coordination Interface support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

// Package psci implements helpers for secondary core bring-up through the
// ARM Power State Coordination Interface (PSCI) CPU_ON service.
//
// A successful CPU_ON only means that the request was accepted, a core is
// usable once it reports that it has arrived, see [Await].
//
// This package is only meant to be used with `GOOS=tamago` as supported by the
// TamaGo framework for bare metal Go, see https://github.com/usbarmory/tamago.
package psci

// CPU_ON return codes (Arm DEN 0022, table "Return error codes").
const (
	SUCCESS            = 0
	NOT_SUPPORTED      = -1
	INVALID_PARAMETERS = -2
	DENIED             = -3
	ALREADY_ON         = -4
	ON_PENDING         = -5
	INTERNAL_FAILURE   = -6
	NOT_PRESENT        = -7
	DISABLED           = -8
	INVALID_ADDRESS    = -9
)

var names = map[int64]string{
	SUCCESS:            "SUCCESS",
	NOT_SUPPORTED:      "NOT_SUPPORTED",
	INVALID_PARAMETERS: "INVALID_PARAMETERS",
	DENIED:             "DENIED",
	ALREADY_ON:         "ALREADY_ON",
	ON_PENDING:         "ON_PENDING",
	INTERNAL_FAILURE:   "INTERNAL_FAILURE",
	NOT_PRESENT:        "NOT_PRESENT",
	DISABLED:           "DISABLED",
	INVALID_ADDRESS:    "INVALID_ADDRESS",
}

// String returns the name of a PSCI return code, or "unknown" for a value the
// specification does not define.
func String(rc int64) string {
	if s, ok := names[rc]; ok {
		return s
	}

	return "unknown"
}

// Started reports whether a CPU_ON return code leaves the target core on, or
// on its way on, so that the caller should wait for it to arrive with [Await].
func Started(rc int64) bool {
	switch rc {
	case SUCCESS, ALREADY_ON, ON_PENDING:
		return true
	}

	return false
}

// Await polls arrived for every core in cores until each has arrived or
// expired reports true, and returns the cores that arrived in the order given.
func Await(cores []int, arrived func(core int) bool, expired func() bool) (ready []int) {
	pending := append([]int(nil), cores...)

	for {
		missing := pending[:0]

		for _, c := range pending {
			if !arrived(c) {
				missing = append(missing, c)
			}
		}

		pending = missing

		if len(pending) == 0 || expired() {
			break
		}
	}

	for _, c := range cores {
		if !contains(pending, c) {
			ready = append(ready, c)
		}
	}

	return
}

func contains(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}

	return false
}
