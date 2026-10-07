// ARM Power State Coordination Interface support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package psci

import (
	"slices"
	"testing"
)

func TestStarted(t *testing.T) {
	for _, tt := range []struct {
		rc      int64
		name    string
		started bool
	}{
		{SUCCESS, "SUCCESS", true},
		{NOT_SUPPORTED, "NOT_SUPPORTED", false},
		{INVALID_PARAMETERS, "INVALID_PARAMETERS", false},
		{DENIED, "DENIED", false},
		{ALREADY_ON, "ALREADY_ON", true},
		{ON_PENDING, "ON_PENDING", true},
		{INTERNAL_FAILURE, "INTERNAL_FAILURE", false},
		{NOT_PRESENT, "NOT_PRESENT", false},
		{DISABLED, "DISABLED", false},
		{INVALID_ADDRESS, "INVALID_ADDRESS", false},
		{-10, "unknown", false},
		{1, "unknown", false},
	} {
		if got := Started(tt.rc); got != tt.started {
			t.Errorf("Started(%d) = %v, want %v", tt.rc, got, tt.started)
		}

		if got := String(tt.rc); got != tt.name {
			t.Errorf("String(%d) = %q, want %q", tt.rc, got, tt.name)
		}
	}
}

func TestAwaitAllArrive(t *testing.T) {
	expired := func() bool {
		t.Fatal("expired consulted although every core had arrived")
		return true
	}

	ready := Await([]int{1, 2, 3}, func(int) bool { return true }, expired)

	if !slices.Equal(ready, []int{1, 2, 3}) {
		t.Errorf("ready = %v, want [1 2 3]", ready)
	}
}

// A core that never arrives must not hang the caller and must not be reported.
func TestAwaitTimesOut(t *testing.T) {
	passes := 0
	expired := func() bool {
		passes++
		return passes >= 3
	}

	ready := Await([]int{1, 2, 3}, func(c int) bool { return c != 2 }, expired)

	if !slices.Equal(ready, []int{1, 3}) {
		t.Errorf("ready = %v, want [1 3]", ready)
	}

	if passes != 3 {
		t.Errorf("expired consulted %d times, want 3", passes)
	}
}

// A core arriving late, but before the deadline, counts.
func TestAwaitLateArrival(t *testing.T) {
	polls := map[int]int{}
	arrived := func(c int) bool {
		polls[c]++
		return c != 3 || polls[c] > 5
	}

	ready := Await([]int{1, 2, 3}, arrived, func() bool { return false })

	if !slices.Equal(ready, []int{1, 2, 3}) {
		t.Errorf("ready = %v, want [1 2 3]", ready)
	}

	if polls[1] != 1 {
		t.Errorf("core 1 polled %d times after it arrived, want 1", polls[1])
	}
}

// The deadline is checked after a pass, so a core arriving on the last pass is
// still counted.
func TestAwaitArrivalAtDeadline(t *testing.T) {
	pass := 0
	arrived := func(c int) bool { return c == 1 || pass >= 1 }
	expired := func() bool {
		pass++
		return pass >= 2
	}

	ready := Await([]int{1, 2}, arrived, expired)

	if !slices.Equal(ready, []int{1, 2}) {
		t.Errorf("ready = %v, want [1 2]", ready)
	}
}

func TestAwaitNone(t *testing.T) {
	if ready := Await(nil, nil, nil); len(ready) != 0 {
		t.Errorf("ready = %v, want none", ready)
	}
}
