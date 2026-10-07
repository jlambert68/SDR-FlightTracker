package location

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Position struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	City      string  `json:"city"`
	Country   string  `json:"country_name"`
}

func Lookup(ctx context.Context) (Position, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://ipapi.co/json/", nil)
	if err != nil {
		return Position{}, err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return Position{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Position{}, fmt.Errorf("location service HTTP %s", response.Status)
	}
	var position Position
	if err := json.NewDecoder(response.Body).Decode(&position); err != nil {
		return Position{}, err
	}
	if position.Latitude == 0 && position.Longitude == 0 {
		return Position{}, fmt.Errorf("location service returned no coordinates")
	}
	return position, nil
}
