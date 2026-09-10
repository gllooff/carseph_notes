package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRateLimiter(t *testing.T) {
	rl := newRateLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		if !rl.allow("1.2.3.4") {
			t.Fatalf("hit %d within limit rejected", i+1)
		}
	}
	if rl.allow("1.2.3.4") {
		t.Fatal("hit beyond limit allowed")
	}
	if !rl.allow("5.6.7.8") {
		t.Fatal("other key rejected")
	}
}

func TestRateLimitMiddleware(t *testing.T) {
	env := testServer(t)
	for i := 0; i < 40; i++ {
		req, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/api/auth/register/begin",
			strings.NewReader(`{"username":"ratelimit","invite_code":"x"}`))
		req.Header.Set("Origin", env.cfg.Origin)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode == http.StatusTooManyRequests {
			if ra := res.Header.Get("Retry-After"); ra == "" {
				t.Error("429 without Retry-After header")
			}
			return // limiter engaged
		}
	}
	t.Fatal("limiter never engaged after 40 requests")
}
