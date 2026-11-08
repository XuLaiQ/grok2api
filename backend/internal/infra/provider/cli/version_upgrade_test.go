package cli

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
)

type buildVersionUpgraderStub struct {
	version string
	changed bool
	err     error
	calls   int
}

func (u *buildVersionUpgraderStub) UpgradeBuildClientVersion(_ context.Context, version string) (string, bool, error) {
	u.calls++
	if u.err != nil {
		return "", false, u.err
	}
	if u.changed {
		u.version = version
	}
	return u.version, u.changed, nil
}

func TestRequiredBuildClientVersion(t *testing.T) {
	const body = `{"error":"Your Grok CLI version (1.0.4) is outdated. Please update to version 1.0.60 or later via ` + "`grok update`" + ` or the installation documentation."}`
	for _, test := range []struct {
		name      string
		status    int
		baseURL   string
		body      string
		want      string
		wantFound bool
	}{
		{name: "official Build minimum", status: http.StatusUpgradeRequired, baseURL: "https://cli-chat-proxy.grok.com/v1", body: body, want: "1.0.60", wantFound: true},
		{name: "official XAI minimum", status: http.StatusUpgradeRequired, baseURL: "https://api.x.ai/v1", body: body, want: "1.0.60", wantFound: true},
		{name: "wrong status", status: http.StatusForbidden, baseURL: "https://cli-chat-proxy.grok.com/v1", body: body},
		{name: "untrusted host", status: http.StatusUpgradeRequired, baseURL: "https://evil.example/v1", body: body},
		{name: "http host", status: http.StatusUpgradeRequired, baseURL: "http://cli-chat-proxy.grok.com/v1", body: body},
		{name: "port host", status: http.StatusUpgradeRequired, baseURL: "https://cli-chat-proxy.grok.com:443/v1", body: body},
		{name: "malformed JSON", status: http.StatusUpgradeRequired, baseURL: "https://cli-chat-proxy.grok.com/v1", body: `{`},
		{name: "no explicit minimum", status: http.StatusUpgradeRequired, baseURL: "https://cli-chat-proxy.grok.com/v1", body: `{"error":"Update grok to latest."}`},
		{name: "prerelease", status: http.StatusUpgradeRequired, baseURL: "https://cli-chat-proxy.grok.com/v1", body: `{"error":"Update to version 1.0.60-beta.1"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := &http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader(test.body))}
			got, ok := requiredBuildClientVersion(response, test.baseURL)
			if got != test.want || ok != test.wantFound {
				t.Fatalf("requiredBuildClientVersion() = %q, %t; want %q, %t", got, ok, test.want, test.wantFound)
			}
			if test.status == http.StatusUpgradeRequired && response.Body != nil {
				restored, err := io.ReadAll(response.Body)
				if err != nil || string(restored) != test.body {
					t.Fatalf("response body was not restored: %q, %v", restored, err)
				}
			}
		})
	}
}

func TestRequiredBuildClientVersionRejectsOversizedBody(t *testing.T) {
	body := `{"error":"` + strings.Repeat("x", 80<<10) + ` update to version 1.0.60"}`
	response := &http.Response{StatusCode: http.StatusUpgradeRequired, Body: io.NopCloser(strings.NewReader(body))}
	if got, ok := requiredBuildClientVersion(response, "https://cli-chat-proxy.grok.com/v1"); ok || got != "" {
		t.Fatalf("oversized response accepted: %q, %t", got, ok)
	}
}

func TestForwardResponseUpgradesAndRetriesOnce(t *testing.T) {
	cipher, err := security.NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	token, err := cipher.Encrypt("access-token")
	if err != nil {
		t.Fatal(err)
	}
	adapter := NewAdapter(Config{BaseURL: "https://cli-chat-proxy.grok.com/v1", ClientVersion: "1.0.4", ClientIdentifier: "grok-shell", TokenAuth: "xai-grok-cli", UserAgent: buildUserAgent("1.0.4")}, cipher)
	upgrader := &buildVersionUpgraderStub{changed: true}
	adapter.SetBuildVersionUpgrader(upgrader)
	calls := 0
	adapter.http.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			if request.Header.Get("x-grok-client-version") != "1.0.4" || request.Header.Get("User-Agent") != buildUserAgent("1.0.4") {
				t.Fatalf("first request fingerprint = %q / %q", request.Header.Get("x-grok-client-version"), request.Header.Get("User-Agent"))
			}
			return &http.Response{StatusCode: http.StatusUpgradeRequired, Status: "426 Upgrade Required", Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":"Your Grok CLI version (1.0.4) is outdated. Please update to version 1.0.60 or later via ` + "`grok update`" + `."}`)), Request: request}, nil
		case 2:
			if request.Header.Get("x-grok-client-version") != "1.0.60" || request.Header.Get("User-Agent") != buildUserAgent("1.0.60") {
				t.Fatalf("retry fingerprint = %q / %q", request.Header.Get("x-grok-client-version"), request.Header.Get("User-Agent"))
			}
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"resp_1"}`)), Request: request}, nil
		default:
			t.Fatalf("request retried too many times: %d", calls)
			return nil, nil
		}
	})

	response, err := adapter.ForwardResponse(context.Background(), provider.ResponseResourceRequest{
		Credential: account.Credential{Provider: account.ProviderBuild, AuthType: account.AuthTypeOAuth, EncryptedAccessToken: token},
		Method:     http.MethodPost, Path: "/responses", Model: "grok-4.7", PromptCacheKey: "version-upgrade-test",
		Body: []byte(`{"model":"grok-4.7","input":"hello"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || calls != 2 || upgrader.calls != 1 {
		t.Fatalf("status=%d requests=%d upgrades=%d", response.StatusCode, calls, upgrader.calls)
	}
}

func TestForwardResponseDoesNotUpgradeWhenPersistenceFails(t *testing.T) {
	cipher, err := security.NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	token, err := cipher.Encrypt("access-token")
	if err != nil {
		t.Fatal(err)
	}
	adapter := NewAdapter(Config{BaseURL: "https://cli-chat-proxy.grok.com/v1", ClientVersion: "1.0.4", ClientIdentifier: "grok-shell", TokenAuth: "xai-grok-cli", UserAgent: buildUserAgent("1.0.4")}, cipher)
	upgrader := &buildVersionUpgraderStub{err: context.DeadlineExceeded}
	adapter.SetBuildVersionUpgrader(upgrader)
	calls := 0
	adapter.http.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusUpgradeRequired, Status: "426 Upgrade Required", Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":"Please update to version 1.0.60 or later."}`)), Request: request}, nil
	})
	response, err := adapter.ForwardResponse(context.Background(), provider.ResponseResourceRequest{
		Credential: account.Credential{Provider: account.ProviderBuild, AuthType: account.AuthTypeOAuth, EncryptedAccessToken: token},
		Method:     http.MethodPost, Path: "/responses", Model: "grok-4.7",
		Body: []byte(`{"model":"grok-4.7","input":"hello"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusUpgradeRequired || calls != 1 || upgrader.calls != 1 || !strings.Contains(string(body), "1.0.60") {
		t.Fatalf("status=%d requests=%d upgrades=%d body=%s", response.StatusCode, calls, upgrader.calls, body)
	}
}
