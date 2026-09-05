package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthIdentifiesManagedProcess(t *testing.T) {
	for _, token := range []string{"", "update-test-process-instance"} {
		t.Run(token, func(t *testing.T) {
			t.Setenv("GROK2API_UPDATE_HEALTH_TOKEN", token)
			router := New(testDependencies())
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
			if recorder.Code != http.StatusOK || recorder.Header().Get("X-Grok2api-Instance") != token {
				t.Fatalf("health response: %d, %v", recorder.Code, recorder.Header())
			}
		})
	}
}

func TestUpdateHealthWaitsForStartupRecovery(t *testing.T) {
	ready := false
	deps := testDependencies()
	deps.TrafficReady = func() bool { return ready }
	router := New(deps)
	for _, state := range []bool{false, true} {
		ready = state
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if recorder.Code != http.StatusOK {
			t.Fatal("startup recovery must not change liveness semantics")
		}
		if got := recorder.Header().Get("X-Grok2api-Startup-Ready") == "true"; got != ready {
			t.Fatalf("startup ready = %v, want %v", got, ready)
		}
	}
}
