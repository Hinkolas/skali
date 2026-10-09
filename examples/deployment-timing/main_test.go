package main

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestRoutesStartAndProtectData(t *testing.T) {
	s := &service{token: "test-token", started: time.Now()}
	handler := s.routes() // Registration must succeed for mixed GET/PUT routes.
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/", 200},
		{"GET", "/health/startup", 200},
		{"GET", "/health/live", 200},
		{"PUT", "/records/example", 401},
		{"GET", "/objects/example", 401},
		{"GET", "/missing", 404},
		{"POST", "/", 405},
	} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.status {
			t.Errorf("%s %s: got %d, want %d", tc.method, tc.path, w.Code, tc.status)
		}
	}
}
