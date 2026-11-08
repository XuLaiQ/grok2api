package settings

import (
	"strings"

	"golang.org/x/mod/semver"
)

func NormalizeStableBuildClientVersion(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 || strings.HasPrefix(value, "v") {
		return "", false
	}
	version := "v" + value
	if !semver.IsValid(version) || semver.Prerelease(version) != "" || semver.Build(version) != "" {
		return "", false
	}
	if len(strings.Split(value, ".")) != 3 {
		return "", false
	}
	return value, true
}

func CompareStableBuildClientVersions(left, right string) (int, bool) {
	left, leftOK := NormalizeStableBuildClientVersion(left)
	right, rightOK := NormalizeStableBuildClientVersion(right)
	if !leftOK || !rightOK {
		return 0, false
	}
	return semver.Compare("v"+left, "v"+right), true
}
