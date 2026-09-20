package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sort"
	"time"

	_ "github.com/lib/pq"
)

var db *sql.DB

type OccupiedInterval struct {
	StartMeters float64 `json:"start_meters"`
	EndMeters   float64 `json:"end_meters"`
	VehicleReg  string  `json:"vehicle_reg,omitempty"`
}

type LaybySlot struct {
	SlotNumber             int     `json:"slot_number"`
	StartMeters            float64 `json:"start_meters"`
	EndMeters              float64 `json:"end_meters"`
	LengthMeters           float64 `json:"length_meters"`
	FitsStandardArtic165m  bool    `json:"fits_standard_artic_16_5m"`
	FitsRigid12m           bool    `json:"fits_rigid_12m"`
}

func calculateLaybySlots(totalLength float64, occupied []OccupiedInterval, bufferMeters float64) []LaybySlot {
	if len(occupied) == 0 {
		if totalLength >= 12.0 {
			return []LaybySlot{
				{
					SlotNumber:            1,
					StartMeters:           0.0,
					EndMeters:             totalLength,
					LengthMeters:          totalLength,
					FitsStandardArtic165m: totalLength >= 16.5,
					FitsRigid12m:          totalLength >= 12.0,
				},
			}
		}
		return []LaybySlot{}
	}

	// Expand occupied intervals with safety buffer and clamp to layby boundaries
	type interval struct {
		start float64
		end   float64
	}
	var blocked []interval
	for _, o := range occupied {
		s := o.StartMeters - bufferMeters
		if s < 0 {
			s = 0
		}
		e := o.EndMeters + bufferMeters
		if e > totalLength {
			e = totalLength
		}
		blocked = append(blocked, interval{start: s, end: e})
	}

	// Sort blocked intervals by start meter
	sort.Slice(blocked, func(i, j int) bool {
		return blocked[i].start < blocked[j].start
	})

	// Merge overlapping blocked intervals
	var merged []interval
	cur := blocked[0]
	for i := 1; i < len(blocked); i++ {
		if blocked[i].start <= cur.end {
			if blocked[i].end > cur.end {
				cur.end = blocked[i].end
			}
		} else {
			merged = append(merged, cur)
			cur = blocked[i]
		}
	}
	merged = append(merged, cur)

	// Identify free gaps
	var slots []LaybySlot
	slotNum := 1
	cursor := 0.0

	for _, b := range merged {
		if b.start > cursor {
			gapLen := b.start - cursor
			if gapLen >= 12.0 {
				slots = append(slots, LaybySlot{
					SlotNumber:            slotNum,
					StartMeters:           cursor,
					EndMeters:             b.start,
					LengthMeters:          gapLen,
					FitsStandardArtic165m: gapLen >= 16.5,
					FitsRigid12m:          true,
				})
				slotNum++
			}
		}
		if b.end > cursor {
			cursor = b.end
		}
	}

	if cursor < totalLength {
		gapLen := totalLength - cursor
		if gapLen >= 12.0 {
			slots = append(slots, LaybySlot{
				SlotNumber:            slotNum,
				StartMeters:           cursor,
				EndMeters:             totalLength,
				LengthMeters:          gapLen,
				FitsStandardArtic165m: gapLen >= 16.5,
				FitsRigid12m:          true,
			})
		}
	}

	return slots
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	var err error
	dbConnStr := os.Getenv("DB_URL")
	if dbConnStr == "" {
		dbConnStr = os.Getenv("DATABASE_URL")
	}
	if dbConnStr != "" {
		db, err = sql.Open("postgres", dbConnStr)
		if err != nil {
			log.Printf("Warning: Failed to connect to DB: %v", err)
		} else {
			initReservationTable(context.Background())
			startReservationSweeper(context.Background(), 1*time.Minute)
		}
	}

	http.HandleFunc("/v1/parking/layby-slots", handleLaybySlots)
	http.HandleFunc("/v1/parking/reserve-slot", handleReserveSlot)
	http.HandleFunc("/v1/parking/expire-sweeper", handleSweepReservations)

	log.Printf("Server listening on port %s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func handleLaybySlots(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"Method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	corridorID := r.URL.Query().Get("corridor_id")
	if corridorID == "" {
		http.Error(w, `{"error":"corridor_id is required"}`, http.StatusBadRequest)
		return
	}

	query := `
		SELECT COALESCE(vehicle_reg, 'UNKNOWN'), start_meter, end_meter
		FROM layby_occupancy_segments
		WHERE layby_id = $1::uuid AND is_confirmed_parked = true
		UNION ALL
		SELECT vehicle_reg, start_meter, end_meter
		FROM layby_reservations
		WHERE layby_id = $1::uuid AND status = 'HELD' AND expires_at > NOW();
	`

	var occupied []OccupiedInterval
	if db != nil {
		rows, err := db.QueryContext(r.Context(), query, corridorID)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var o OccupiedInterval
				if err := rows.Scan(&o.VehicleReg, &o.StartMeters, &o.EndMeters); err == nil {
					occupied = append(occupied, o)
				}
			}
		} else {
			log.Printf("Query error: %v", err)
		}
	}

	totalLength := 120.0
	safetyBuffer := 2.0

	slots := calculateLaybySlots(totalLength, occupied, safetyBuffer)

	status := "SPACES_AVAILABLE"
	if len(slots) == 0 {
		status = "FULL"
	}

	resp := map[string]interface{}{
		"layby_id":               corridorID,
		"name":                   "A14 Eastbound J7 Kettering",
		"total_length_meters":    totalLength,
		"safety_buffer_meters":   safetyBuffer,
		"occupied_vehicle_count": len(occupied),
		"available_slots":        slots,
		"capacity_status":        status,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
