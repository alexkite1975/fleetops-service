package main
import ("context"; "database/sql"; "encoding/json"; "fmt"; "log"; "math"; "net/http"; "os"; "strconv"; "strings"; "time"; _ "github.com/lib/pq")
var db *sql.DB
type Defect struct { ID string `json:"id"`; VehicleReg string `json:"vehicle_reg"`; TrailerID *string `json:"trailer_id,omitempty"`; DepotID string `json:"depot_id"`; DepotName string `json:"depot_name"`; DriverID string `json:"driver_id"`; DriverName string `json:"driver_name"`; Severity string `json:"severity"`; Category string `json:"category"`; Description string `json:"description"`; PhotoURLs []string `json:"photo_urls"`; Status string `json:"status"`; LoggedAt time.Time `json:"logged_at"`; Latitude float64 `json:"latitude"`; Longitude float64 `json:"longitude"`; DistanceKm *float64 `json:"distance_km,omitempty"` }
type BridgeHazard struct { ID string `json:"id"`; RoadName string `json:"road_name"`; ClearanceMeters float64 `json:"clearance_meters"`; ClearanceImperial string `json:"clearance_imperial"`; LocationDescription string `json:"location_description"`; DistanceMeters float64 `json:"distance_meters"` }
type BackhaulLoad struct { ID string `json:"id"`; OrderNumber string `json:"order_number"`; CollectionLocation string `json:"collection_location"`; DeliveryLocation string `json:"delivery_location"`; OfferedPriceGBP float64 `json:"offered_price_gbp"`; TotalDistanceMiles float64 `json:"total_distance_miles"`; RatePerMileGBP float64 `json:"rate_per_mile_gbp"`; DeadheadMiles float64 `json:"deadhead_miles"`; EstimatedDriveMins int `json:"estimated_drive_mins"`; IsTachoSafe bool `json:"is_tacho_safe"`; TachoMarginMinutes int `json:"tacho_margin_minutes"` }
type DVLACheckReq struct { DriverID string `json:"driver_id"`; CheckCode string `json:"check_code"`; NINO string `json:"nino"` }
type ExpenseReq struct { DriverID string `json:"driver_id"`; Category string `json:"category"`; Vendor string `json:"vendor"`; GrossAmount float64 `json:"gross_amount"`; VatAmount float64 `json:"vat_amount"`; VolumeLitres *float64 `json:"volume_litres,omitempty"`; ReceiptURL string `json:"receipt_url"`; OCRConfidence float64 `json:"ocr_confidence"` }
type PanicReq struct { DriverID string `json:"driver_id"`; AlertType string `json:"alert_type"`; Latitude float64 `json:"latitude"`; Longitude float64 `json:"longitude"`; W3W string `json:"w3w"` }
type AxleCheckReq struct { TractorTareKg float64 `json:"tractor_tare_kg"`; TrailerTareKg float64 `json:"trailer_tare_kg"`; PayloadKg float64 `json:"payload_kg"`; PayloadCoGFromKingpinMeters float64 `json:"payload_cog_meters"`; TrailerWheelbaseMeters float64 `json:"trailer_wheelbase_meters"`; FifthWheelSlideOffsetMeters float64 `json:"fifth_wheel_offset_meters"` }
func initDB() { dbURL := os.Getenv("DB_URL"); if dbURL == "" { log.Fatal("DB_URL missing") }; var err error; db, err = sql.Open("postgres", dbURL); if err != nil { log.Fatalf("DB open error: %v", err) }; db.SetMaxOpenConns(25); db.SetMaxIdleConns(5); db.SetConnMaxLifetime(15 * time.Minute); ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second); defer cancel(); if err := db.PingContext(ctx); err != nil { log.Printf("DB ping warning: %v", err) } else { log.Println("DB connected.") } }
func scanDefects(rows *sql.Rows) []Defect { var defects []Defect; for rows.Next() { var d Defect; var p string; var dist sql.NullFloat64; _ = rows.Scan(&d.ID, &d.VehicleReg, &d.TrailerID, &d.DepotID, &d.DepotName, &d.DriverID, &d.DriverName, &d.Severity, &d.Category, &d.Description, &p, &d.Status, &d.LoggedAt, &d.Latitude, &d.Longitude, &dist); if p != "" { t := strings.Trim(p, "{}"); if t != "" { d.PhotoURLs = strings.Split(t, ",") } }; if d.PhotoURLs == nil { d.PhotoURLs = []string{} }; if dist.Valid { v := dist.Float64; d.DistanceKm = &v }; defects = append(defects, d) }; if defects == nil { defects = []Defect{} }; return defects }
func handleHealth(w http.ResponseWriter, r *http.Request) { w.Header().Set("Content-Type", "application/json"); w.WriteHeader(http.StatusOK); w.Write([]byte(`{"status":"healthy","service":"fleetops-api"}`)) }
func handleDefects(w http.ResponseWriter, r *http.Request) { if r.Method != http.MethodGet { http.Error(w, `{"error":"Method not allowed"}`, 405); return }; latStr := r.URL.Query().Get("lat"); lngStr := r.URL.Query().Get("lng"); radStr := r.URL.Query().Get("radius_km"); limStr := r.URL.Query().Get("limit"); limit := 20; if limStr != "" { if l, err := strconv.Atoi(limStr); err == nil && l > 0 && l <= 100 { limit = l } }; w.Header().Set("Content-Type", "application/json"); if latStr != "" && lngStr != "" && radStr != "" { lat, _ := strconv.ParseFloat(latStr, 64); lng, _ := strconv.ParseFloat(lngStr, 64); rad, _ := strconv.ParseFloat(radStr, 64); q := "SELECT COALESCE(vehicle_reg, 'UNKNOWN'), start_meter, end_meter
	FROM layby_occupancy_segments
	WHERE layby_id = $1::uuid AND is_confirmed_parked = true
	UNION ALL
	SELECT vehicle_reg, start_meter, end_meter
	FROM layby_reservations
	WHERE layby_id = $1::uuid AND status = 'HELD' AND expires_at > NOW();", laybyID)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"query error: %v"}`, err), 500)
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
	bufferMeters := 2.0
	slots := calculateLaybySlots(totalLength, occupied, bufferMeters)
	if slots == nil {
		slots = []AvailableSlot{}
	}
	status := "FULL"
	if len(slots) > 0 {
		status = "SPACES_AVAILABLE"
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(LaybySlotResponse{
		LaybyID:            laybyID,
		Name:               name,
		TotalLengthMeters:  totalLength,
		SafetyBufferMeters: bufferMeters,
		OccupiedSpaces:     len(occupied),
		AvailableSlots:     slots,
		CapacityStatus:     status,
	})
}
