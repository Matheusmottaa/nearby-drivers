package matching

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Matheusmottaa/nearby-drivers/internal/geo"
)

const (
	defaultTimeout = 800 * time.Millisecond
	defaultRetries = 2
	maxBody        = 1 << 20
)

// ErrBreakerOpen means the circuit is open and the call was not attempted.
var ErrBreakerOpen = errors.New("circuit breaker open")

// NearbyDriver is one candidate returned by GET /drivers/nearby.
type NearbyDriver struct {
	ID  string
	Lat float64
	Lng float64
}

// Estimate is one origin in a POST /routes/eta:batch response.
type Estimate struct {
	OriginIndex int
	DurationS   int
	DistanceM   int
	Reachable   bool
}

// NearbyAPI lists candidates around a pickup.
type NearbyAPI interface {
	Nearby(ctx context.Context, traceID string, pickup geo.Point, radiusM float64, k int, class string) ([]NearbyDriver, error)
}

// ETAAPI estimates travel from each origin to the pickup.
type ETAAPI interface {
	Batch(ctx context.Context, traceID string, dest geo.Point, origins []geo.Point, radiusM float64) ([]Estimate, error)
}

// ClientOptions tunes the shared retry policy. Retries counts extra attempts
// after the first call. A zero Timeout or negative Retries selects the defaults.
type ClientOptions struct {
	Timeout time.Duration
	Retries int
	Sleep   func(context.Context, time.Duration) error
	Now     func() time.Time
}

// DefaultClientOptions is the policy from the stage: 800 ms and two jittered retries.
func DefaultClientOptions() ClientOptions {
	return ClientOptions{Timeout: defaultTimeout, Retries: defaultRetries}
}

type caller struct {
	base    string
	http    *http.Client
	breaker *Breaker
	timeout time.Duration
	retries int
	sleep   func(context.Context, time.Duration) error
}

func newCaller(base string, opt ClientOptions) caller {
	if opt.Timeout <= 0 {
		opt.Timeout = defaultTimeout
	}
	if opt.Retries < 0 {
		opt.Retries = defaultRetries
	}
	if opt.Sleep == nil {
		opt.Sleep = sleepCtx
	}
	return caller{
		base:    strings.TrimRight(base, "/"),
		http:    &http.Client{},
		breaker: NewBreaker(opt.Now),
		timeout: opt.Timeout,
		retries: opt.Retries,
		sleep:   opt.Sleep,
	}
}

// NearbyClient calls GET /drivers/nearby.
type NearbyClient struct{ caller }

// NewNearbyClient returns the driver-service client.
func NewNearbyClient(base string, opt ClientOptions) *NearbyClient {
	return &NearbyClient{caller: newCaller(base, opt)}
}

// ETAClient calls POST /routes/eta:batch.
type ETAClient struct{ caller }

// NewETAClient returns the routing-service client.
func NewETAClient(base string, opt ClientOptions) *ETAClient {
	return &ETAClient{caller: newCaller(base, opt)}
}

// BreakerOpen reports whether the routing circuit is refusing calls.
func (c *ETAClient) BreakerOpen() bool {
	return c.breaker.State() == breakerOpen
}

func (c *NearbyClient) Nearby(ctx context.Context, traceID string, pickup geo.Point, radiusM float64, k int, class string) ([]NearbyDriver, error) {
	q := url.Values{}
	q.Set("lat", strconv.FormatFloat(pickup.Lat, 'f', -1, 64))
	q.Set("lng", strconv.FormatFloat(pickup.Lng, 'f', -1, 64))
	q.Set("k", strconv.Itoa(k))
	q.Set("radius_m", strconv.FormatFloat(radiusM, 'f', -1, 64))
	q.Set("class", class)
	raw, err := c.call(ctx, traceID, func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/drivers/nearby?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	var body struct {
		Drivers []struct {
			ID  string  `json:"id"`
			Lat float64 `json:"lat"`
			Lng float64 `json:"lng"`
		} `json:"drivers"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("decode nearby: %w", err)
	}
	out := make([]NearbyDriver, len(body.Drivers))
	for i, d := range body.Drivers {
		out[i] = NearbyDriver{ID: d.ID, Lat: d.Lat, Lng: d.Lng}
	}
	return out, nil
}

func (c *ETAClient) Batch(ctx context.Context, traceID string, dest geo.Point, origins []geo.Point, radiusM float64) ([]Estimate, error) {
	payload := struct {
		Destination geoPoint   `json:"destination"`
		Origins     []geoPoint `json:"origins"`
		MaxRadiusM  float64    `json:"max_radius_m"`
	}{
		Destination: geoPoint{Lat: dest.Lat, Lng: dest.Lng},
		Origins:     make([]geoPoint, len(origins)),
		MaxRadiusM:  radiusM,
	}
	for i, o := range origins {
		payload.Origins[i] = geoPoint{Lat: o.Lat, Lng: o.Lng}
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode eta: %w", err)
	}
	raw, err := c.call(ctx, traceID, func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/routes/eta:batch", bytes.NewReader(buf))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	var body struct {
		Estimates []struct {
			OriginIndex int  `json:"origin_index"`
			DurationS   int  `json:"duration_s"`
			DistanceM   int  `json:"distance_m"`
			Reachable   bool `json:"reachable"`
		} `json:"estimates"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("decode eta: %w", err)
	}
	out := make([]Estimate, len(body.Estimates))
	for i, e := range body.Estimates {
		out[i] = Estimate{OriginIndex: e.OriginIndex, DurationS: e.DurationS, DistanceM: e.DistanceM, Reachable: e.Reachable}
	}
	return out, nil
}

type geoPoint struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

func (c caller) call(ctx context.Context, traceID string, build func(context.Context) (*http.Request, error)) ([]byte, error) {
	var last error
	for attempt := 0; attempt <= c.retries; attempt++ {
		if attempt > 0 {
			if err := c.sleep(ctx, jitter(attempt)); err != nil {
				return nil, err
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !c.breaker.Allow() {
			return nil, ErrBreakerOpen
		}
		reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
		req, err := build(reqCtx)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("build request: %w", err)
		}
		if traceID != "" {
			req.Header.Set("X-Request-Id", traceID)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			cancel()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			c.breaker.Failure()
			last = err
			continue
		}
		raw, err := readLimited(resp)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			c.breaker.Failure()
			last = err
			continue
		}
		if retryable(resp.StatusCode) {
			c.breaker.Failure()
			last = fmt.Errorf("status %d", resp.StatusCode)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			c.breaker.Success()
			return nil, fmt.Errorf("status %d", resp.StatusCode)
		}
		c.breaker.Success()
		return raw, nil
	}
	if last == nil {
		last = errors.New("call failed")
	}
	return nil, last
}

func retryable(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}

func readLimited(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return raw, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func jitter(attempt int) time.Duration {
	ceiling := time.Duration(attempt) * 50 * time.Millisecond
	if ceiling <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(ceiling)))
}
