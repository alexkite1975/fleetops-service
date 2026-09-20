package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
)

type ReserveSlotRequest struct {
	LaybyID      string `json:"layby_id"`
	VehicleReg   string `json:"vehicle_reg"`
	VehicleType  string `json:"vehicle_type"`
	DurationMins int    `json:"duration_minutes"`
}

type ReserveSlotResponse struct {
	ReservationID string  `json:"reservation_id"`
	LaybyID       string  `json:"layby_id"`
	VehicleReg    string  `json:"vehicle_reg"`
	StartMeter    float64 `json:"start_meter"`
	EndMeter      float64 `json:"end_meter"`
	LengthMeters  float64 `json:"length_meters"`
	ExpiresAt     string  `json:"expires_at"`
	Status        string  `json:"status"`
}

func initReservationTable(ctx context.Context) error {
	if db == nil {
		return nil
	}
	query := `
	CREATE TABLE IF NOT EXISTS layby_reservations (
		id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		layby_id UUID NOT NULL REFERENCES layby_corridors(id) ON DELETE CASCADE,
		vehicle_reg VARCHAR(32) NOT NULL,
		start_meter NUMERIC(6, 2) NOT NULL,
		end_meter NUMERIC(6, 2) NOT NULL,
		reserved_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		expires_at TIMESTAMPTZ NOT NULL DEFAULT (NOW() + INTERVAL '15 minutes'),
		status VARCHAR(32) NOT NULL DEFAULT 'HELD',
		CONSTRAINT check_reservation_meters CHECK (end_meter > start_meter)
	);
	CREATE INDEX IF NOT EXISTS idx_layby_res_active ON layby_reservations(layby_id, expires_at) WHERE status = 'HELD';
	`
	_, err := db.ExecContext(ctx, query)
	return err
}

func handleReserveSlot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"Method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req ReserveSlotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request payload"}`, http.StatusBadRequest)
		return
	}

	if req.LaybyID == "" || req.VehicleReg == "" {
		http.Error(w, `{"error":"layby_id and vehicle_reg are required"}`, http.StatusBadRequest)
		return
	}

	requiredLength := 18.5
	if req.VehicleType == "RIGID" {
		requiredLength = 14.0
	}
	if req.DurationMins <= 0 || req.DurationMins > 60 {
		req.DurationMins = 15
	}

	tx, err := db.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, `{"error":"failed to start transaction"}`, http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	var totalLength float64
	err = tx.QueryRowContext(r.Context(), "SELECT total_length_meters FROM layby_corridors WHERE id = $1::uuid FOR UPDATE;", req.LaybyID).Scan(&totalLength)
	if err == sql.ErrNoRows {
		http.Error(w, `{"error":"layby corridor not found"}`, http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"db error: %v"}`, err), http.StatusInternalServerError)
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
	rows, err := tx.QueryContext(r.Context(), query, req.LaybyID)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"query error: %v"}`, err), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var occupied []OccupiedInterval
	for rows.Next() {
		var o OccupiedInterval
		if err := rows.Scan(&o.VehicleReg, &o.StartMeters, &o.EndMeters); err == nil {
			if o.StartMeters > o.EndMeters {
				o.StartMeters, o.EndMeters = o.EndMeters, o.StartMeters
			}
			occupied = append(occupied, o)
		}
	}

	slots := calculateLaybySlots(totalLength, occupied, 2.0)
	var chosenSlot *AvailableSlot
	for _, s := range slots {
		if s.LengthMeters >= requiredLength {
			slotCopy := s
			chosenSlot = &slotCopy
			break
		}
	}

	if chosenSlot == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":"NO_SLOT_AVAILABLE","message":"No available slot large enough for requested vehicle"}`))
		return
	}

	startMeter := chosenSlot.StartMeters
	endMeter := math.Round((startMeter+requiredLength)*10) / 10

	var reservationID string
	var expiresAt string
	insertQuery := `
		INSERT INTO layby_reservations (layby_id, vehicle_reg, start_meter, end_meter, expires_at)
		VALUES ($1::uuid, $2, $3, $4, NOW() + ($5 || ' minutes')::interval)
		RETURNING id, to_char(expires_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"');
	`
	err = tx.QueryRowContext(r.Context(), insertQuery, req.LaybyID, req.VehicleReg, startMeter, endMeter, req.DurationMins).Scan(&reservationID, &expiresAt)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"insert reservation error: %v"}`, err), http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, `{"error":"commit failed"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(ReserveSlotResponse{
		ReservationID: reservationID,
		LaybyID:       req.LaybyID,
		VehicleReg:    req.VehicleReg,
		StartMeter:    startMeter,
		EndMeter:      endMeter,
		LengthMeters:  requiredLength,
		ExpiresAt:     expiresAt,
		Status:        "HELD",
	})
}
