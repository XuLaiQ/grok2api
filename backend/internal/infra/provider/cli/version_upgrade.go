package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	settingsdomain "github.com/chenyme/grok2api/backend/internal/domain/settings"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
)

var requiredClientVersionPattern = regexp.MustCompile(`(?i)\bupdate to version ([0-9]+\.[0-9]+\.[0-9]+)([^0-9A-Za-z.+-]|$)`)

func requiredBuildClientVersion(response *http.Response, baseURL string) (string, bool) {
	if response == nil || response.StatusCode != http.StatusUpgradeRequired || response.Body == nil || !isOfficialBuildHost(baseURL) {
		return "", false
	}
	body, truncated, err := provider.ReadDiagnosticBody(response.Body)
	response.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil || truncated {
		return "", false
	}
	var payload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &payload) != nil || strings.TrimSpace(payload.Error) == "" {
		return "", false
	}
	match := requiredClientVersionPattern.FindStringSubmatch(payload.Error)
	if len(match) != 3 {
		return "", false
	}
	if match[2] != " " && match[2] != "\t" && match[2] != "\r" && match[2] != "\n" && match[2] != "," && match[2] != ")" && match[2] != "!" && match[2] != "?" {
		return "", false
	}
	version, ok := settingsdomain.NormalizeStableBuildClientVersion(match[1])
	if !ok {
		return "", false
	}
	return version, true
}

func isOfficialBuildHost(baseURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.User != nil || parsed.Port() != "" {
		return false
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "cli-chat-proxy.grok.com", "api.x.ai":
		return true
	default:
		return false
	}
}

func buildUserAgent(version string) string {
	return "grok-shell/" + version + " (linux; x86_64)"
}
