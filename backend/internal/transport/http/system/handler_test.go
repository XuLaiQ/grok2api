package system

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	updatecheckapp "github.com/chenyme/grok2api/backend/internal/application/updatecheck"
	"github.com/gin-gonic/gin"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestHandlerReturnsOnlyPublicFrontendConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	NewHandler(func() string { return "https://api.example.com/" }, updatecheckapp.NewService("v3.0.0", nil)).Register(router.Group("/api/admin/v1"))
	request := httptest.NewRequest(http.MethodGet, "/api/admin/v1/system", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var payload struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data) != 1 || payload.Data["publicApiBaseURL"] != "https://api.example.com" {
		t.Fatalf("data = %#v", payload.Data)
	}
}

func TestHandlerReturnsAndChecksVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"tag_name":"v3.0.1","body":"Notes"}`))}, nil
	})}
	router := gin.New()
	updates := updatecheckapp.NewService("v3.0.0", client, updatecheckapp.Options{Repository: "chenyme/grok2api"})
	NewHandler(nil, updates).Register(router.Group("/api/admin/v1"))

	for _, test := range []struct {
		method string
		path   string
		status string
	}{
		{method: http.MethodGet, path: "/api/admin/v1/system/version", status: "unchecked"},
		{method: http.MethodPost, path: "/api/admin/v1/system/update/check", status: "update_available"},
	} {
		request := httptest.NewRequest(test.method, test.path, nil)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s %s status = %d", test.method, test.path, recorder.Code)
		}
		var payload struct {
			Data struct {
				CurrentVersion string `json:"currentVersion"`
				Status         string `json:"status"`
			} `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Data.CurrentVersion != "v3.0.0" || payload.Data.Status != test.status {
			t.Fatalf("%s %s data = %#v", test.method, test.path, payload.Data)
		}
	}
}

func TestUpdateConfigurationAndStatusRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	updates := updatecheckapp.NewService("v3.0.0", nil, updatecheckapp.Options{Directory: t.TempDir()})
	router := gin.New()
	NewHandler(nil, updates).Register(router.Group("/api/admin/v1"))
	for _, test := range []struct {
		method, path, body string
		status             int
	}{
		{http.MethodPut, "/system/update/config", `{"repository":"https://github.com/owner/repo.git"}`, http.StatusOK},
		{http.MethodGet, "/system/update/status", "", http.StatusOK},
		{http.MethodPut, "/system/update/config", `{"repository":"https://evil.example/repo"}`, http.StatusBadRequest},
		{http.MethodPut, "/system/update/config", `{}`, http.StatusBadRequest},
		{http.MethodPut, "/system/update/config", `{"repository":"owner/repo","command":"arbitrary"}`, http.StatusBadRequest},
		{http.MethodPost, "/system/update/install", `{"version":"v3.0.1","url":"https://evil.example"}`, http.StatusBadRequest},
		{http.MethodPost, "/system/update/install", `{}`, http.StatusBadRequest},
		{http.MethodPost, "/system/update/install", `{"version":"v3.0.1"}`, http.StatusServiceUnavailable},
		{http.MethodPut, "/system/update/config", `{"repository":"owner/repo"} {}`, http.StatusBadRequest},
	} {
		request := httptest.NewRequest(test.method, "/api/admin/v1"+test.path, strings.NewReader(test.body))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if recorder.Code != test.status {
			t.Errorf("%s %s %s status=%d body=%s", test.method, test.path, test.body, recorder.Code, recorder.Body.String())
		}
	}
	if updates.Snapshot().Repository != "owner/repo" {
		t.Fatal("invalid requests changed repository configuration")
	}
}
