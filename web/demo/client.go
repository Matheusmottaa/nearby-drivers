package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type client struct {
	ride   string
	driver string
	http   *http.Client
}

func newClient(rideURL, driverURL string) *client {
	return &client{
		ride:   strings.TrimRight(rideURL, "/"),
		driver: strings.TrimRight(driverURL, "/"),
		http:   &http.Client{Timeout: 20 * time.Second},
	}
}

func (c *client) ready(ctx context.Context) bool {
	if c == nil || c.ride == "" || c.driver == "" {
		return false
	}
	return c.ok(ctx, c.ride+"/readyz") && c.ok(ctx, c.driver+"/readyz")
}

func (c *client) ok(ctx context.Context, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}

type driverBody struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type registration struct {
	Name         string `json:"name"`
	Phone        string `json:"phone"`
	VehicleClass string `json:"vehicle_class"`
	Plate        string `json:"plate"`
}

type locationBody struct {
	Lat        float64   `json:"lat"`
	Lng        float64   `json:"lng"`
	Seq        int64     `json:"seq"`
	ObservedAt time.Time `json:"observed_at"`
	AccuracyM  float64   `json:"accuracy_m"`
}

type rideBody struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Pickup struct {
		Lat float64 `json:"lat"`
		Lng float64 `json:"lng"`
	} `json:"pickup"`
	Driver *struct {
		ID        string `json:"id"`
		ETAS      int    `json:"eta_s"`
		DistanceM int    `json:"distance_m"`
		Engine    string `json:"eta_engine"`
	} `json:"driver"`
}

type createRideBody struct {
	PassengerID  string `json:"passenger_id"`
	Pickup       latlng `json:"pickup"`
	VehicleClass string `json:"vehicle_class"`
}

type latlng struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

func (c *client) createDriver(ctx context.Context, idem string, in registration) (driverBody, error) {
	var out driverBody
	_, err := c.do(ctx, http.MethodPost, c.driver+"/drivers", in, map[string]string{"Idempotency-Key": idem}, &out)
	return out, err
}

func (c *client) patchStatus(ctx context.Context, id, status string) (int, error) {
	return c.do(ctx, http.MethodPatch, c.driver+"/drivers/"+id+"/status", map[string]string{"status": status}, nil, nil)
}

func (c *client) putLocation(ctx context.Context, id string, fix locationBody) error {
	_, err := c.do(ctx, http.MethodPut, c.driver+"/drivers/"+id+"/location", fix, nil, nil)
	return err
}

func (c *client) getDriver(ctx context.Context, id string) (driverBody, int, error) {
	var out driverBody
	code, err := c.do(ctx, http.MethodGet, c.driver+"/drivers/"+id, nil, nil, &out)
	return out, code, err
}

func (c *client) createRide(ctx context.Context, idem, trace string, body createRideBody) (rideBody, error) {
	var out rideBody
	_, err := c.do(ctx, http.MethodPost, c.ride+"/rides", body, map[string]string{
		"Idempotency-Key": idem,
		"X-Request-Id":    trace,
	}, &out)
	return out, err
}

func (c *client) getRide(ctx context.Context, id string) (rideBody, error) {
	var out rideBody
	_, err := c.do(ctx, http.MethodGet, c.ride+"/rides/"+id, nil, nil, &out)
	return out, err
}

func (c *client) cancelRide(ctx context.Context, id, reason string) (rideBody, error) {
	var out rideBody
	_, err := c.do(ctx, http.MethodPost, c.ride+"/rides/"+id+"/cancel", map[string]string{"reason": reason}, nil, &out)
	return out, err
}

func (c *client) do(ctx context.Context, method, url string, body any, headers map[string]string, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, fmt.Errorf("encode %s: %w", url, err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return 0, fmt.Errorf("build %s: %w", url, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s %s: %w", method, url, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("read %s: %w", url, err)
	}
	if resp.StatusCode >= 300 {
		detail := strings.TrimSpace(string(raw))
		if len(detail) > 300 {
			detail = detail[:300]
		}
		return resp.StatusCode, fmt.Errorf("%s %s: %d %s", method, url, resp.StatusCode, detail)
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode %s: %w", url, err)
		}
	}
	return resp.StatusCode, nil
}
