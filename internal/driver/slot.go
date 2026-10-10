package driver

import (
	"sync"
	"time"

	"github.com/Matheusmottaa/nearby-drivers/internal/geo"
	"github.com/Matheusmottaa/nearby-drivers/internal/kdtree"
)

// HeartbeatWindow is how long a fix keeps a driver in the nearby search.
const HeartbeatWindow = 30 * time.Second

// Slot is one driver's position in the dense in-memory array.
// InternalID is the array index. Lat, lng, seq and the heartbeat are not columns.
type Slot struct {
	InternalID uint32
	DriverID   string
	Class      string
	Lat        float64
	Lng        float64
	Seq        int64
	Heartbeat  time.Time
	HasFix     bool
	Available  bool
	Reserved   bool
	RideID     string
	Lost       bool
}

// ApplyLocation writes the fix when seq is newer than the one already applied.
// An equal or older seq leaves the slot unchanged.
func (s *Slot) ApplyLocation(lat, lng float64, seq int64, observed time.Time) bool {
	if s.HasFix && seq <= s.Seq {
		return false
	}
	s.Lat = lat
	s.Lng = lng
	s.Seq = seq
	s.Heartbeat = observed
	s.HasFix = true
	return true
}

// Fresh reports whether the heartbeat is inside the last 30s at now.
// A fix observed exactly 30s ago is still fresh.
func (s Slot) Fresh(now time.Time) bool {
	if !s.HasFix || s.Heartbeat.IsZero() {
		return false
	}
	age := now.Sub(s.Heartbeat)
	return age <= HeartbeatWindow
}

// Eligible is the nearby predicate: declared AVAILABLE, heartbeat inside the
// window, and no reservation.
func (s Slot) Eligible(now time.Time) bool {
	return s.Available && !s.Reserved && s.Fresh(now)
}

// Assign reserves the driver for rideID. The same ride is a no-op and keeps
// the lost mark. A different current reservation is left alone.
func (s *Slot) Assign(rideID string) {
	if s.Reserved {
		return
	}
	s.Reserved = true
	s.RideID = rideID
	s.Lost = false
	s.Available = true
}

// Release clears the reservation when rideID is the current one.
// A release for any other ride does not change the slot.
func (s *Slot) Release(rideID string) bool {
	if !s.Reserved || s.RideID != rideID {
		return false
	}
	s.Reserved = false
	s.RideID = ""
	s.Lost = false
	s.Available = true
	return true
}

// LostDue reports whether a sweeper should publish lost for this reservation.
// An available driver who only stopped sending heartbeats is not lost: the
// fresh bit drops them from the index. Each reservation is published once.
func (s Slot) LostDue(now time.Time) bool {
	return s.DriverID != "" && s.Reserved && !s.Lost && s.RideID != "" && !s.Fresh(now)
}

// MarkLost records that lost was already published for the current reservation.
func (s *Slot) MarkLost() {
	if s.Reserved {
		s.Lost = true
	}
}

func newSlot(d Driver) Slot {
	return Slot{
		InternalID: d.InternalID,
		DriverID:   d.ID,
		Class:      d.VehicleClass,
		Available:  d.Status == StatusAvailable || d.Status == StatusOnTrip,
		Reserved:   d.Status == StatusOnTrip,
		RideID:     d.CurrentRideID,
	}
}

// table is the dense slot array. The index of a slot is its internal_id.
type table struct {
	mu    sync.RWMutex
	slots []Slot
	byID  map[string]uint32
}

func newTable() *table {
	return &table{byID: make(map[string]uint32)}
}

func (t *table) Load(drivers []Driver) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.byID = make(map[string]uint32, len(drivers))
	if len(drivers) == 0 {
		t.slots = nil
		return
	}
	var maxID uint32
	for _, d := range drivers {
		if d.InternalID > maxID {
			maxID = d.InternalID
		}
	}
	t.slots = make([]Slot, maxID+1)
	for _, d := range drivers {
		t.slots[d.InternalID] = newSlot(d)
		t.byID[d.ID] = d.InternalID
	}
}

func (t *table) Insert(d Driver) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.grow(d.InternalID)
	t.slots[d.InternalID] = newSlot(d)
	t.byID[d.ID] = d.InternalID
}

func (t *table) grow(id uint32) {
	if int(id) < len(t.slots) {
		return
	}
	next := make([]Slot, id+1)
	copy(next, t.slots)
	t.slots = next
}

func (t *table) Get(id string) (Slot, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	idx, ok := t.byID[id]
	if !ok || int(idx) >= len(t.slots) {
		return Slot{}, false
	}
	return t.slots[idx], true
}

func (t *table) ByInternal(id uint32) (Slot, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if int(id) >= len(t.slots) {
		return Slot{}, false
	}
	sl := t.slots[id]
	if sl.DriverID == "" {
		return Slot{}, false
	}
	return sl, true
}

func (t *table) Update(id string, fn func(*Slot)) (Slot, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	idx, ok := t.byID[id]
	if !ok || int(idx) >= len(t.slots) {
		return Slot{}, false
	}
	fn(&t.slots[idx])
	return t.slots[idx], true
}

type dueReservation struct {
	DriverID string
	RideID   string
}

func (t *table) DueLost(now time.Time) []dueReservation {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var out []dueReservation
	for _, sl := range t.slots {
		if sl.LostDue(now) {
			out = append(out, dueReservation{DriverID: sl.DriverID, RideID: sl.RideID})
		}
	}
	return out
}

// Entries builds the snapshot from the slot positions and the declared status
// just read from the database. Drivers without a fresh available fix are omitted;
// publishing replaces the previous tree.
func (t *table) Entries(rows []Driver, now time.Time) []kdtree.Entry {
	t.mu.RLock()
	defer t.mu.RUnlock()
	byInternal := make(map[uint32]Driver, len(rows))
	for _, d := range rows {
		byInternal[d.InternalID] = d
	}
	out := make([]kdtree.Entry, 0, len(rows))
	for i := range t.slots {
		sl := t.slots[i]
		if sl.DriverID == "" || !sl.HasFix {
			continue
		}
		row, ok := byInternal[sl.InternalID]
		if !ok || row.Status != StatusAvailable || !sl.Fresh(now) {
			continue
		}
		out = append(out, kdtree.Entry{
			ID:        sl.InternalID,
			Point:     geo.Point{Lat: sl.Lat, Lng: sl.Lng},
			Class:     row.VehicleClass,
			Available: true,
		})
	}
	return out
}
