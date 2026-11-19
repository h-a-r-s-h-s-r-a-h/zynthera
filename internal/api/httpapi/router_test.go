package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"zynthera/internal/api/appconfig"
	"zynthera/internal/shared/httpx"
	"zynthera/internal/shared/logging"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func testDeps(db, redis error) Deps {
	return Deps{
		Config: appconfig.Config{WebOrigin: "http://localhost:3000"},
		Logger: logging.NewTo(io.Discard, "test"),
		DB:     fakePinger{db},
		Redis:  fakePinger{redis},
	}
}

func TestHealth(t *testing.T) {
	cases := []struct {
		name       string
		db, redis  error
		wantStatus int
		wantState  string
	}{
		{"all up", nil, nil, 200, "ok"},
		{"postgres down", errors.New("boom"), nil, 503, "degraded"},
		{"redis down", nil, errors.New("boom"), 503, "degraded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewRouter(testDeps(tc.db, tc.redis))
			for _, path := range []string{"/health", "/api/health"} {
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
				if rec.Code != tc.wantStatus {
					t.Fatalf("%s: status = %d, want %d", path, rec.Code, tc.wantStatus)
				}
				var body struct {
					Status string `json:"status"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body.Status != tc.wantState {
					t.Errorf("%s: state = %q, want %q", path, body.Status, tc.wantState)
				}
				if strings.Contains(rec.Body.String(), "boom") {
					t.Errorf("%s: response leaks the internal error", path)
				}
				if rec.Header().Get("X-Request-Id") == "" {
					t.Errorf("%s: no request id header", path)
				}
				if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
					t.Errorf("%s: security headers missing", path)
				}
			}
		})
	}
}

func TestNotFoundUsesEnvelope(t *testing.T) {
	h := NewRouter(testDeps(nil, nil))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/nope", nil))
	if rec.Code != 404 {
		t.Fatalf("status = %d", rec.Code)
	}
	var body httpx.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "not_found" || body.Error.Details == nil {
		t.Errorf("unexpected envelope: %s", rec.Body.String())
	}
}

func TestOriginCheck(t *testing.T) {
	r := chi.NewRouter()
	r.Use(originCheck("http://localhost:3000"))
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }
	r.Get("/x", ok)
	r.Post("/x", ok)

	cases := []struct {
		name   string
		method string
		origin string
		fetch  string
		want   int
	}{
		{"GET needs nothing", "GET", "", "", 204},
		{"GET from elsewhere is still fine", "GET", "https://evil.example", "", 204},
		{"POST with matching origin", "POST", "http://localhost:3000", "", 204},
		{"POST with wrong origin", "POST", "https://evil.example", "", 403},
		{"POST with wrong origin but same-origin fetch header", "POST", "https://evil.example", "same-origin", 403},
		{"POST with no origin and no fetch header", "POST", "", "", 403},
		{"POST with no origin, same-origin fetch", "POST", "", "same-origin", 204},
		{"POST with no origin, cross-site fetch", "POST", "", "cross-site", 403},
		{"POST with null origin", "POST", "null", "", 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/x", nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.fetch != "" {
				req.Header.Set("Sec-Fetch-Site", tc.fetch)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestLimitBody(t *testing.T) {
	h := limitBody(8)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			w.WriteHeader(413)
			return
		}
		w.WriteHeader(204)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/", strings.NewReader("tiny")))
	if rec.Code != 204 {
		t.Errorf("small body: status = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/", strings.NewReader("much too large a body")))
	if rec.Code != 413 {
		t.Errorf("large body: status = %d", rec.Code)
	}
}

func TestRecoverReturnsEnvelope(t *testing.T) {
	h := httpx.Recover(logging.NewTo(io.Discard, "test"))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("secret detail")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 500 {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "secret detail") {
		t.Error("panic value leaked to the client")
	}
}
