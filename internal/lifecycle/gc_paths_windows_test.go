//go:build windows

package lifecycle

import "testing"

func TestPathComparisonsAreCaseInsensitiveOnWindowsVolumes(t *testing.T) {
	if !samePathForPlatform(`C:\Pool\Slot`, `c:\pool\slot`, true) {
		t.Fatal("samePathForPlatform did not fold Windows volume/path case")
	}
	if !pathContainsForPlatform(`C:\Pool\Slot`, `c:\pool\slot\src`, true) {
		t.Fatal("pathContainsForPlatform did not protect a case-variant subdirectory")
	}
	if pathContainsForPlatform(`C:\Pool\Slot`, `c:\pool\slot-other`, true) {
		t.Fatal("pathContainsForPlatform treated a sibling as a child")
	}
}
