package main

import (
	"testing"
)

func TestCalculateLaybySlots(t *testing.T) {
	tests := []struct {
		name         string
		totalLength  float64
		bufferMeters float64
		occupied     []OccupiedInterval
		expectedLen  int
		expectStatus string
	}{
		{
			name:         "Empty 100m layby",
			totalLength:  100.0,
			bufferMeters: 2.0,
			occupied:     []OccupiedInterval{},
			expectedLen:  1,
		},
		{
			name:         "Fully packed layby (no gaps >= 12m)",
			totalLength:  60.0,
			bufferMeters: 2.0,
			occupied: []OccupiedInterval{
				{StartMeters: 0.0, EndMeters: 18.0, VehicleReg: "TRUCK1"},
				{StartMeters: 20.0, EndMeters: 38.0, VehicleReg: "TRUCK2"},
				{StartMeters: 40.0, EndMeters: 58.0, VehicleReg: "TRUCK3"},
			},
			expectedLen: 0,
		},
		{
			name:         "Two artics with middle gap (A14 Kettering baseline)",
			totalLength:  120.0,
			bufferMeters: 2.0,
			occupied: []OccupiedInterval{
				{StartMeters: 0.0, EndMeters: 18.5, VehicleReg: "KX68 HGV"},
				{StartMeters: 50.0, EndMeters: 68.5, VehicleReg: "FJ21 TRK"},
			},
			expectedLen: 2,
		},
		{
			name:         "Overlapping parked spans merge correctly",
			totalLength:  100.0,
			bufferMeters: 2.0,
			occupied: []OccupiedInterval{
				{StartMeters: 10.0, EndMeters: 25.0, VehicleReg: "REG_A"},
				{StartMeters: 24.0, EndMeters: 40.0, VehicleReg: "REG_B"},
			},
			expectedLen: 1, // cursor to 10m is 9m usable (<12m threshold), 41m to 100m is 59m usable (1 slot)
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			slots := calculateLaybySlots(tc.totalLength, tc.occupied, tc.bufferMeters)
			if len(slots) != tc.expectedLen {
				t.Fatalf("expected %d slots, got %d", tc.expectedLen, len(slots))
			}
		})
	}
}
