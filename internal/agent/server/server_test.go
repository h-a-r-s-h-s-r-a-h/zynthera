package server

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"

	"zynthera/internal/agent/agentconfig"
	"zynthera/internal/shared/logging"
)

func TestHealth(t *testing.T) {
	h := NewRouter(Deps{
		Config: agentconfig.Config{VMDriver: "mock"},
		Logger: logging.NewTo(io.Discard, "test"),
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" || body["driver"] != "mock" || body["version"] != Version {
		t.Errorf("unexpected body: %v", body)
	}
}

func TestUnknownRoute(t *testing.T) {
	h := NewRouter(Deps{Logger: logging.NewTo(io.Discard, "test")})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/nope", nil))
	if rec.Code != 404 {
		t.Fatalf("status = %d", rec.Code)
	}
}
