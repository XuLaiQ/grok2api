package settings

import "testing"

func TestNormalizeStableBuildClientVersion(t *testing.T) {
	for _, test := range []struct{ value, want string }{{"1.0.13", "1.0.13"}, {" 1.0.60 ", "1.0.60"}} {
		if normalized, ok := NormalizeStableBuildClientVersion(test.value); !ok || normalized != test.want {
			t.Fatalf("NormalizeStableBuildClientVersion(%q) = %q, %t; want %q", test.value, normalized, ok, test.want)
		}
	}
	for _, value := range []string{"", "v1.0.13", "1.0", "1.0.13-beta.1", "1.0.13+build.2", "01.0.13", "1.0.13.1"} {
		if normalized, ok := NormalizeStableBuildClientVersion(value); ok {
			t.Fatalf("NormalizeStableBuildClientVersion(%q) = %q, want invalid", value, normalized)
		}
	}
}

func TestCompareStableBuildClientVersions(t *testing.T) {
	if comparison, ok := CompareStableBuildClientVersions("1.0.60", "1.0.13"); !ok || comparison <= 0 {
		t.Fatalf("upgrade comparison = %d, %t", comparison, ok)
	}
	if comparison, ok := CompareStableBuildClientVersions("1.0.13", "1.0.60"); !ok || comparison >= 0 {
		t.Fatalf("downgrade comparison = %d, %t", comparison, ok)
	}
	if comparison, ok := CompareStableBuildClientVersions("1.0.13", "1.0.13"); !ok || comparison != 0 {
		t.Fatalf("equal comparison = %d, %t", comparison, ok)
	}
}
