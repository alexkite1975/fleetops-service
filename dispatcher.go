package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
)

type DispatcherOverview struct {
	CorridorID           string        `json:"corridor_id"`
	CorridorName         string        `json:"corridor_name"`
	TotalLengthMeters    float64       `json:"total_length_meters"`
	OccupiedVehicleCount int           `json:"occupied_vehicle_count"`
	ActiveHolds          []Reservation `json:"active_holds"`
	AvailableSlotsCount  int           `json:"available_slots_count"`
}

type Reservation struct {
	ID          string  `json:"id"`
	VehicleReg  string  `json:"vehicle_reg"`
	VehicleType string  `json:"vehicle_type"`
	Status      string  `json:"status"`
	StartMeters float64 `json:"start_meters"`
	EndMeters   float64 `json:"end_meters"`
	ExpiresAt   string  `json:"expires_at"`
}

// HandleDispatcherOverview returns a comprehensive view for fleet managers
func HandleDispatcherOverview(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if db == nil {
			http.Error(w, `{"error":"Database unavailable"}`, http.StatusServiceUnavailable)
			w.Header().Set("Content-Type", "application/json")
			return
		}

		corridorID := r.URL.Query().Get("corridor_id")
		if corridorID == "" {
			corridorID = "a1400000-0000-0000-0000-000000000001"
		}

		var name string
		var totalLen float64
		err := db.QueryRow("SELECT name, total_length_meters FROM corridors WHERE id = $1", corridorID).Scan(&name, &totalLen)
		if err != nil {
			name = "A14 Eastbound J7 Kettering"
			totalLen = 120.0
		}

		rows, err := db.Query(`
			SELECT id, vehicle_reg, vehicle_type, status, start_meters, end_meters, expires_at 
			FROM layby_reservations 
			WHERE layby_id = $1 AND status IN ('HELD', 'CONFIRMED')
			ORDER BY start_meters ASC`, corridorID)
		
		var holds []Reservation
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var h Reservation
				var expStr string
				if err := rows.Scan(&h.ID, &h.VehicleReg, &h.VehicleType, &h.Status, &h.StartMeters, &h.EndMeters, &expStr); err == nil {
					h.ExpiresAt = expStr
					holds = append(holds, h)
				}
			}
		}

		overview := DispatcherOverview{
			CorridorID:           corridorID,
			CorridorName:         name,
			TotalLengthMeters:    totalLen,
			OccupiedVehicleCount: len(holds),
			ActiveHolds:          holds,
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(overview)
	}
}

// sendTwilioSMS sends an alert if configured
func sendTwilioSMS(toNum, message string) error {
	accountSid := os.Getenv("TWILIO_ACCOUNT_SID")
	authToken := os.Getenv("TWILIO_AUTH_TOKEN")
	fromNum := os.Getenv("TWILIO_FROM_NUMBER")

	if accountSid == "" || authToken == "" || fromNum == "" || toNum == "" {
		log.Println("[Twilio] Skipping SMS: credentials or destination missing")
		return nil
	}

	apiURL := "https://api.twilio.com/2010-04-01/Accounts/" + accountSid + "/Messages.json"
	
	data := url.Values{}
	data.Set("To", toNum)
	data.Set("From", fromNum)
	data.Set("Body", message)

	req, _ := http.NewRequest("POST", apiURL, strings.NewReader(data.Encode()))
	req.SetBasicAuth(accountSid, authToken)
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	log.Printf("[Twilio] SMS alert sent to %s (Status: %s)", toNum, resp.Status)
	return nil
}
