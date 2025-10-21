package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

var serpAPIKey = "ee8d486346a3443077233121815c938582af95b2c7cc647e619841bdfb96d215"

// Request/Response Structs for Dialogflow webhook
type WebhookRequest struct {
	SessionInfo struct {
		Parameters map[string]interface{} `json:"parameters"`
	} `json:"sessionInfo"`
}

type FlightOption struct {
	ID             int         `json:"id"`
	Airline        string      `json:"airline"`
	Airplane       string      `json:"airplane"`
	Price          interface{} `json:"price"`
	DepartureDate  string      `json:"departure_date"`
	Origin         string      `json:"origin"`
	Destination    string      `json:"destination"`
	Duration       interface{} `json:"duration"`
	TravelClass    string      `json:"travel_class"`
	IsNonstop      bool        `json:"is_nonstop"`
	ConnectionInfo interface{} `json:"connection_info"`
}

type FulfillmentResponse struct {
	Messages []struct {
		Text struct {
			Text []string `json:"text"`
		} `json:"text"`
	} `json:"messages"`
}

type WebhookResponse struct {
	FulfillmentResponse struct {
		Messages []struct {
			Text struct {
				Text []string `json:"text"`
			} `json:"text"`
		} `json:"messages"`
	} `json:"fulfillmentResponse"`
	SessionInfo struct {
		Parameters map[string]interface{} `json:"parameters,omitempty"`
	} `json:"sessionInfo,omitempty"`
}

// Helper to write JSON with CORS
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Access-Control-Max-Age", "3600")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// Extract city code (first 3 letters upper)
func extractCityCode(param interface{}) string {
	switch v := param.(type) {
	case map[string]interface{}:
		if city, ok := v["city"]; ok {
			if s, ok := city.(string); ok && len(s) >= 3 {
				return strings.ToUpper(s[:3])
			}
		}
		if orig, ok := v["original"]; ok {
			if s, ok := orig.(string); ok && len(s) >= 3 {
				return strings.ToUpper(s[:3])
			}
		}
	case string:
		if len(v) >= 3 {
			return strings.ToUpper(v[:3])
		}
	}
	return "UNK"
}

// Parse departure_date param as dict or string
func parseDepartureDate(param interface{}) string {
	now := time.Now()
	defaultDate := now.Add(7 * 24 * time.Hour)
	if param == nil {
		return defaultDate.Format("2006-01-02")
	}
	switch v := param.(type) {
	case map[string]interface{}:
		year := int(now.Year())
		month := int(now.Month())
		day := int(now.Day())
		if y, ok := v["year"].(float64); ok {
			year = int(y)
		}
		if m, ok := v["month"].(float64); ok {
			month = int(m)
		}
		if d, ok := v["day"].(float64); ok {
			day = int(d)
		}
		t, err := time.Parse("2006-01-02", fmt.Sprintf("%04d-%02d-%02d", year, month, day))
		if err != nil {
			log.Printf("Invalid dict date: %v", err)
			return defaultDate.Format("2006-01-02")
		}
		return t.Format("2006-01-02")
	case string:
		_, err := time.Parse("2006-01-02", v)
		if err != nil {
			log.Printf("Invalid string date: %v", err)
			return defaultDate.Format("2006-01-02")
		}
		return v
	default:
		return defaultDate.Format("2006-01-02")
	}
}

// Handler for webhook
func SkyscannerWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		writeJSON(w, http.StatusNoContent, nil)
		return
	}

	var req WebhookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Printf("Failed to parse request: %v", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
		return
	}

	params := req.SessionInfo.Parameters
	departureCode := extractCityCode(params["departure_city"])
	destinationCode := extractCityCode(params["destination_city"])
	departureDate := parseDepartureDate(params["departure_date"])

	depDateObj, _ := time.Parse("2006-01-02", departureDate)
	returnDate := depDateObj.Add(7 * 24 * time.Hour).Format("2006-01-02")

	adults := 1
	if val, ok := params["passenger_count"]; ok {
		switch v := val.(type) {
		case float64:
			adults = int(v)
		case string:
			if i, err := strconv.Atoi(v); err == nil {
				adults = i
			}
		}
	}

	// SerpAPI request
	apiURL := "https://serpapi.com/search"
	reqURL := fmt.Sprintf("%s?engine=google_flights&departure_id=%s&arrival_id=%s&outbound_date=%s&return_date=%s&adults=%d&currency=USD&api_key=%s",
		apiURL, departureCode, destinationCode, departureDate, returnDate, adults, serpAPIKey)

	resp, err := http.Get(reqURL)
	if err != nil {
		log.Printf("SerpAPI error: %v", err)
		writeJSON(w, 500, map[string]string{"error": "SerpAPI request failed"})
		return
	}
	defer resp.Body.Close()

	var serpData map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&serpData); err != nil {
		log.Printf("Failed to decode SerpAPI response: %v", err)
		writeJSON(w, 500, map[string]string{"error": "Invalid SerpAPI response"})
		return
	}

	// Extract best and other flights
	bestFlights, _ := serpData["best_flights"].([]interface{})
	otherFlights, _ := serpData["other_flights"].([]interface{})
	allFlights := append(bestFlights, otherFlights...)

	messageLines := []string{}
	flightOptions := []FlightOption{}

	if len(allFlights) == 0 {
		messageLines = append(messageLines, fmt.Sprintf("No flights found from %s to %s on %s.", departureCode, destinationCode, departureDate))
	} else {
		messageLines = append(messageLines, fmt.Sprintf("Found flights from %s to %s on %s:", departureCode, destinationCode, departureDate))
		for i, f := range allFlights {
			if i >= 5 {
				break
			}
			flightGroup := f.(map[string]interface{})
			flightsArr, _ := flightGroup["flights"].([]interface{})
			if len(flightsArr) == 0 {
				continue
			}
			first := flightsArr[0].(map[string]interface{})

			airline := fmt.Sprintf("%v", first["airline"])
			price := flightGroup["price"]
			depTime := ""
			if da, ok := first["departure_airport"].(map[string]interface{}); ok {
				if t, ok := da["time"].(string); ok {
					depTime = t
				}
			}
			airplane := fmt.Sprintf("%v", first["airplane"])
			duration := flightGroup["total_duration"]
			if duration == nil {
				duration = flightGroup["duration"]
			}
			travelClass := fmt.Sprintf("%v", first["travel_class"])

			isNonstop := len(flightsArr) == 1
			var connectionInfo interface{} = nil
			if !isNonstop {
				stops := []string{}
				for si := 0; si < len(flightsArr)-1; si++ {
					if arr, ok := flightsArr[si].(map[string]interface{})["arrival_airport"].(map[string]interface{}); ok {
						if name, ok := arr["name"].(string); ok {
							stops = append(stops, name)
						}
					}
				}
				connectionInfo = fmt.Sprintf("%d stop(s): %s", len(stops), strings.Join(stops, ", "))
			}

			messageLines = append(messageLines, fmt.Sprintf("%d. %s (%s) Dep: %s, Price: USD %v, Duration: %v, Class: %s, %s",
				i+1, airline, airplane, depTime, price, duration, travelClass,
				func() string {
					if isNonstop {
						return "Non-stop"
					}
					return fmt.Sprintf("%v", connectionInfo)
				}(),
			))

			flightOptions = append(flightOptions, FlightOption{
				ID:             i + 1,
				Airline:        airline,
				Airplane:       airplane,
				Price:          price,
				DepartureDate:  departureDate,
				Origin:         departureCode,
				Destination:    destinationCode,
				Duration:       duration,
				TravelClass:    travelClass,
				IsNonstop:      isNonstop,
				ConnectionInfo: connectionInfo,
			})
		}
	}

	respBody := WebhookResponse{}
	respBody.FulfillmentResponse.Messages = []struct {
		Text struct {
			Text []string `json:"text"`
		} `json:"text"`
	}{
		{Text: struct {
			Text []string `json:"text"`
		}{Text: messageLines}},
	}
	respBody.SessionInfo.Parameters = map[string]interface{}{
		"flight_comparison_results": flightOptions,
		"origin":                    departureCode,
		"destination":               destinationCode,
	}

	writeJSON(w, 200, respBody)
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	http.HandleFunc("/", SkyscannerWebhook)
	log.Printf("Server running on port %s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
