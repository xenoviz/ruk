package update

import (
	"fmt"
	"strconv"
	"strings"
)

// Version implements SemVer precedence, including numeric and textual
// prerelease identifiers. Build metadata is intentionally rejected because
// release tags and readiness manifests use the canonical public version.
type Version struct {
	Major, Minor, Patch uint64
	Prerelease          []string
}

func ParseVersion(value string) (Version, error) {
	value = strings.TrimPrefix(value, "v")
	if strings.Contains(value, "+") {
		return Version{}, fmt.Errorf("Unsupported version %s", value)
	}
	parts := strings.SplitN(value, "-", 2)
	if len(parts) == 0 || parts[0] == "" {
		return Version{}, fmt.Errorf("Unsupported version %s", value)
	}
	core := strings.Split(parts[0], ".")
	if len(core) != 3 {
		return Version{}, fmt.Errorf("Unsupported version %s", value)
	}
	parsed := Version{}
	values := []*uint64{&parsed.Major, &parsed.Minor, &parsed.Patch}
	for index, text := range core {
		if text == "" || (len(text) > 1 && text[0] == '0') {
			return Version{}, fmt.Errorf("Unsupported version %s", value)
		}
		number, err := strconv.ParseUint(text, 10, 64)
		if err != nil {
			return Version{}, fmt.Errorf("Unsupported version %s", value)
		}
		*values[index] = number
	}
	if len(parts) == 2 {
		if parts[1] == "" {
			return Version{}, fmt.Errorf("Unsupported version %s", value)
		}
		for _, identifier := range strings.Split(parts[1], ".") {
			if identifier == "" || !isSemVerIdentifier(identifier) || (isNumeric(identifier) && len(identifier) > 1 && identifier[0] == '0') {
				return Version{}, fmt.Errorf("Unsupported version %s", value)
			}
			parsed.Prerelease = append(parsed.Prerelease, identifier)
		}
	}
	return parsed, nil
}

func isSemVerIdentifier(value string) bool {
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'A' || character > 'Z') &&
			(character < 'a' || character > 'z') && character != '-' {
			return false
		}
	}
	return value != ""
}

func (version Version) String() string {
	value := fmt.Sprintf("%d.%d.%d", version.Major, version.Minor, version.Patch)
	if len(version.Prerelease) != 0 {
		value += "-" + strings.Join(version.Prerelease, ".")
	}
	return value
}

func (version Version) Compare(other Version) int {
	for _, pair := range [][2]uint64{{version.Major, other.Major}, {version.Minor, other.Minor}, {version.Patch, other.Patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	if len(version.Prerelease) == 0 || len(other.Prerelease) == 0 {
		if len(version.Prerelease) == len(other.Prerelease) {
			return 0
		}
		if len(version.Prerelease) == 0 {
			return 1
		}
		return -1
	}
	for index := 0; index < len(version.Prerelease) && index < len(other.Prerelease); index++ {
		left, right := version.Prerelease[index], other.Prerelease[index]
		if left == right {
			continue
		}
		leftNumeric, rightNumeric := isNumeric(left), isNumeric(right)
		if leftNumeric && rightNumeric {
			left = strings.TrimLeft(left, "0")
			right = strings.TrimLeft(right, "0")
			if left == "" {
				left = "0"
			}
			if right == "" {
				right = "0"
			}
			if len(left) < len(right) {
				return -1
			}
			if len(left) > len(right) {
				return 1
			}
			if left < right {
				return -1
			}
			if left > right {
				return 1
			}
			continue
		}
		if leftNumeric != rightNumeric {
			if leftNumeric {
				return -1
			}
			return 1
		}
		if left < right {
			return -1
		}
		return 1
	}
	if len(version.Prerelease) < len(other.Prerelease) {
		return -1
	}
	if len(version.Prerelease) > len(other.Prerelease) {
		return 1
	}
	return 0
}

func CompareVersions(left, right string) (int, error) {
	leftVersion, err := ParseVersion(left)
	if err != nil {
		return 0, err
	}
	rightVersion, err := ParseVersion(right)
	if err != nil {
		return 0, err
	}
	return leftVersion.Compare(rightVersion), nil
}

func isNumeric(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
