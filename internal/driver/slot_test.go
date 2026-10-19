package driver

import (
	"testing"
	"time"
)

func TestSlotStaleSeq(t *testing.T) {
	var sl Slot
	at := time.Date(2026, 9, 30, 18, 0, 0, 0, time.UTC)
	if !sl.ApplyLocation(-23.56, -46.65, 2, at) {
		t.Fatal("first fix must apply")
	}
	if sl.ApplyLocation(1, 2, 2, at.Add(time.Second)) {
		t.Fatal("equal seq must not apply")
	}
	if sl.ApplyLocation(1, 2, 1, at.Add(2*time.Second)) {
		t.Fatal("older seq must not apply")
	}
	if sl.Lat != -23.56 || sl.Lng != -46.65 || sl.Seq != 2 || !sl.Heartbeat.Equal(at) {
		t.Fatalf("slot changed on stale seq: %+v", sl)
	}
	if !sl.ApplyLocation(-23.57, -46.66, 3, at.Add(time.Second)) {
		t.Fatal("newer seq must apply")
	}
	if sl.Seq != 3 || sl.Lat != -23.57 {
		t.Fatalf("newer seq: %+v", sl)
	}
}

func TestSlotHeartbeatWindow(t *testing.T) {
	now := time.Date(2026, 9, 30, 18, 0, 0, 0, time.UTC)
	var sl Slot
	sl.Available = true
	if !sl.ApplyLocation(-23.56, -46.65, 1, now.Add(-HeartbeatWindow)) {
		t.Fatal("apply")
	}
	if !sl.Fresh(now) || !sl.Eligible(now) {
		t.Fatal("a heartbeat of exactly 30s is still inside the window")
	}

	sl.Heartbeat = now.Add(-HeartbeatWindow - time.Millisecond)
	if sl.Fresh(now) || sl.Eligible(now) {
		t.Fatal("a heartbeat older than 30s must leave the nearby search")
	}
	if sl.LostDue(now) {
		t.Fatal("an available driver without a reservation does not publish lost")
	}
}

func TestSlotReservationAndForeignRelease(t *testing.T) {
	now := time.Date(2026, 9, 30, 18, 0, 0, 0, time.UTC)
	sl := Slot{DriverID: "d1", Available: true}
	if !sl.ApplyLocation(-23.56, -46.65, 1, now) {
		t.Fatal("apply")
	}
	if !sl.Eligible(now) {
		t.Fatal("available driver with a fresh heartbeat is eligible")
	}

	sl.Assign("ride-a")
	if !sl.Reserved || sl.RideID != "ride-a" || sl.Eligible(now) {
		t.Fatalf("reservation bit: %+v", sl)
	}
	sl.Assign("ride-b")
	if sl.RideID != "ride-a" {
		t.Fatal("a second ride must not steal the reservation")
	}
	if sl.Release("ride-b") {
		t.Fatal("release of another ride must be ignored")
	}
	if !sl.Reserved || sl.RideID != "ride-a" {
		t.Fatalf("foreign release changed the slot: %+v", sl)
	}
	if !sl.Release("ride-a") || sl.Reserved || sl.RideID != "" {
		t.Fatalf("matching release: %+v", sl)
	}
	if !sl.Eligible(now) {
		t.Fatal("released driver with a fresh heartbeat returns to nearby")
	}
}

func TestSlotLostOncePerReservation(t *testing.T) {
	now := time.Date(2026, 9, 30, 18, 0, 0, 0, time.UTC)
	sl := Slot{DriverID: "d1", Available: true}
	sl.Assign("ride-a")
	sl.ApplyLocation(-23.56, -46.65, 1, now.Add(-HeartbeatWindow-time.Second))

	if !sl.LostDue(now) {
		t.Fatal("reserved driver without a heartbeat is lost")
	}
	sl.MarkLost()
	if sl.LostDue(now) {
		t.Fatal("lost is published once per reservation")
	}
	sl.Assign("ride-a")
	if sl.LostDue(now) {
		t.Fatal("the same reservation must keep the lost mark")
	}

	if !sl.Release("ride-a") {
		t.Fatal("release")
	}
	sl.Assign("ride-b")
	if !sl.LostDue(now) {
		t.Fatal("a new reservation can publish lost again")
	}
}

func TestStatusTransition(t *testing.T) {
	ok := [][2]string{
		{StatusPending, StatusAvailable},
		{StatusPending, StatusOffline},
		{StatusPending, StatusSuspended},
		{StatusOffline, StatusAvailable},
		{StatusAvailable, StatusOffline},
		{StatusAvailable, StatusSuspended},
		{StatusSuspended, StatusAvailable},
		{StatusAvailable, StatusAvailable},
	}
	for _, pair := range ok {
		if !CanTransit(pair[0], pair[1]) {
			t.Errorf("CanTransit(%s, %s) = false", pair[0], pair[1])
		}
	}
	bad := [][2]string{
		{StatusAvailable, StatusOnTrip},
		{StatusOnTrip, StatusAvailable},
		{StatusOnTrip, StatusOffline},
		{StatusAvailable, StatusPending},
		{StatusOffline, StatusPending},
		{StatusPending, StatusOnTrip},
		{StatusOnTrip, StatusOnTrip},
	}
	for _, pair := range bad {
		if CanTransit(pair[0], pair[1]) {
			t.Errorf("CanTransit(%s, %s) = true", pair[0], pair[1])
		}
	}
}

func TestInternalIDIsTheSlotIndex(t *testing.T) {
	tb := newTable()
	tb.Load([]Driver{
		{ID: "a", InternalID: 0, VehicleClass: "standard", Status: StatusAvailable},
		{ID: "c", InternalID: 2, VehicleClass: "comfort", Status: StatusOnTrip, CurrentRideID: "ride-1"},
	})
	if len(tb.slots) != 3 {
		t.Fatalf("len = %d, want 3", len(tb.slots))
	}
	if tb.slots[0].DriverID != "a" || tb.slots[1].DriverID != "" || tb.slots[2].DriverID != "c" {
		t.Fatalf("slots = %+v", tb.slots)
	}
	if !tb.slots[2].Reserved || tb.slots[2].RideID != "ride-1" || !tb.slots[2].Available {
		t.Fatalf("on trip slot = %+v", tb.slots[2])
	}
	if tb.Available() != 1 {
		t.Fatalf("available = %d, want 1", tb.Available())
	}
	got, ok := tb.Get("c")
	if !ok || got.InternalID != 2 {
		t.Fatalf("get = %+v %v", got, ok)
	}
}
