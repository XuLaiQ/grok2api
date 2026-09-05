package updatecheck

import (
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
)

type semanticVersion struct{ value, prerelease string }

func parseSemanticVersion(value string) (semanticVersion, bool) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "v") {
		value = "v" + value
	}
	core := strings.SplitN(strings.SplitN(value, "+", 2)[0], "-", 2)[0]
	if !semver.IsValid(value) || len(strings.Split(core, ".")) != 3 {
		return semanticVersion{}, false
	}
	return semanticVersion{value: value, prerelease: strings.TrimPrefix(semver.Prerelease(value), "-")}, true
}

func compareSemanticVersion(left, right semanticVersion) int {
	leftBase := strings.SplitN(strings.SplitN(left.value, "+", 2)[0], "-", 2)[0]
	rightBase := strings.SplitN(strings.SplitN(right.value, "+", 2)[0], "-", 2)[0]
	if result := semver.Compare(leftBase, rightBase); result != 0 {
		return result
	}
	// Existing project releases use hotfix.N after a stable release of the same base.
	leftHotfix, leftNumber := projectHotfix(left.prerelease)
	rightHotfix, rightNumber := projectHotfix(right.prerelease)
	if leftHotfix && rightHotfix {
		if leftNumber < rightNumber {
			return -1
		}
		if leftNumber > rightNumber {
			return 1
		}
		return 0
	}
	if leftHotfix {
		return 1
	}
	if rightHotfix {
		return -1
	}
	return semver.Compare(left.value, right.value)
}

func projectHotfix(value string) (bool, uint64) {
	if !strings.HasPrefix(value, "hotfix.") {
		return false, 0
	}
	part := strings.TrimPrefix(value, "hotfix.")
	if part == "" || (len(part) > 1 && part[0] == '0') {
		return false, 0
	}
	number, err := strconv.ParseUint(part, 10, 64)
	return err == nil, number
}
