package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

type ReserveSlotRequest struct {
	LaybyID         string `json:"layby_id"`
	VehicleReg      string `json:"vehicle_reg"`
	VehicleType     string `json:"vehicle_type"`
	DurationMinutes int    `json:"duration_minutes"`
}

type ReservationResponse struct {
	ReservationID string    `json:"reservation_id"`
	LaybyID       string    `json:"layby_id"`
	VehicleReg    string    `json:"vehicle_reg"`
	StartMeter    float64   `json:"start_meter"`
	EndMeter      float64   `json:"end_meter"`
	LengthMeters  float64   `json:"length_meters"`
	ExpiresAt     time.Time `json:"expires_at"`
	Status        string    `json:"status"`
}

func initReservationTable(ctx context.Context) {
	if db == nil {
		return
	}
	schema := `
	CREATE TABLE IF NOT EXISTS layby_reservations (
		id UUID PRIMARY KEY,
		layby_id UUID NOT NULL,
		vehicle_reg VARCHAR(32) NOT NULL,
		start_meter NUMERIC(6,2) NOT NULL,
		end_meter NUMERIC(6,2) NOT NULL,
		length_meters NUMERIC(6,2) NOT NULL,
		status VARCHAR(16) NOT NULL DEFAULT 'HELD',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		expires_at TIMESTAMPTZ NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_layby_res_active 
	ON layby_reservations (layby_id, status, expires_at);
	`
	if _, err := db.ExecContext(ctx, schema); err != nil {
		log.Printf("Warning: Failed to ensure layby_reservations schema: %v", err)
	}
}

func handleReserveSlot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"Method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req ReserveSlotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"Invalid request payload"}`, http.StatusBadRequest)
		return
	}

	if req.LaybyID == "" || req.VehicleReg == "" {
		http.Error(w, `{"error":"layby_id and vehicle_reg are required"}`, http.StatusBadRequest)
		return
	}

	duration := req.DurationMinutes
	if duration <= 0 || duration > 60 {
		duration = 15
	}

	neededLength := 18.5
	if req.VehicleType == "RIGID" {
		neededLength = 14.0
	}

	if db == nil {
		http.Error(w, `{"error":"Database unavailable"}`, http.StatusServiceUnavailable)
		return
	}

	tx, err := db.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, `{"error":"Failed to initiate transaction"}`, http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

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
		http.Error(w, fmt.Sprintf(`{"error":"Query failed: %v"}`, err), http.StatusInternalServerError)
		return
	}

	var occupied []OccupiedInterval
	for rows.Next() {
		var o OccupiedInterval
		if err := rows.Scan(&o.VehicleReg, &o.StartMeters, &o.EndMeters); err == nil {
			occupied = append(occupied, o)
		}
	}
	rows.Close()

	totalLength := 120.0
	safetyBuffer := 2.0
	availableSlots := calculateLaybySlots(totalLength, occupied, safetyBuffer)

	var chosenSlot *LaybySlot
	for i := range availableSlots {
		if availableSlots[i].LengthMeters >= neededLength {
			chosenSlot = &availableSlots[i]
			break
		}
	}

	if chosenSlot == nil {
		http.Error(w, `{"error":"NO_SUITABLE_SLOT_AVAILABLE"}`, http.StatusConflict)
		return
	}

	allocStart := chosenSlot.StartMeters
	allocEnd := allocStart + neededLength
	resUUID := newUUID()
	expiresAt := time.Now().UTC().Add(time.Duration(duration) * time.Minute)

	insertSQL := `
		INSERT INTO layby_reservations (id, layby_id, vehicle_reg, start_meter, end_meter, length_meters, status, expires_at)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, 'HELD', $7);
	`
	_, err = tx.ExecContext(r.Context(), insertSQL, resUUID, req.LaybyID, req.VehicleReg, allocStart, allocEnd, neededLength, expiresAt)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"Insert failed: %v"}`, err), http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, `{"error":"Commit failed"}`, http.StatusInternalServerError)
		return
	}

	resp := ReservationResponse{
		ReservationID: resUUID,
		LaybyID:       req.LaybyID,
		VehicleReg:    req.VehicleReg,
		StartMeter:    allocStart,
		EndMeter:      allocEnd,
		LengthMeters:  neededLength,
		ExpiresAt:     expiresAt,
		Status:        "HELD",
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

func sweepExpiredReservations(ctx context.Context) (int64, error) {
	if db == nil {
		return 0, nil
	}
	query := `
		UPDATE layby_reservations
		SET status = 'EXPIRED'
		WHERE status = 'HELD' AND expires_at <= NOW();
	`
	res, err := db.ExecContext(ctx, query)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func handleSweepReservations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"Method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	rows, err := sweepExpiredReservations(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"sweeper failed: %v"}`, err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"status":"ok","expired_count":%d}`, rows)
}

func startReservationSweeper(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweepExpiredReservations(context.Background())
			}
		}
	}()
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
