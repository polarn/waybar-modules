// Package hwmon provides helpers for discovering and reading Linux hwmon
// temperature sensors under /sys/class/hwmon.
package hwmon

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const hwmonGlob = "/sys/class/hwmon/hwmon*"

// FindByName returns paths of hwmon directories whose "name" file equals
// the given name. Results are sorted by hwmon index for determinism.
func FindByName(name string) ([]string, error) {
	dirs, err := filepath.Glob(hwmonGlob)
	if err != nil {
		return nil, err
	}
	var matches []string
	for _, d := range dirs {
		n, err := Name(d)
		if err != nil {
			continue
		}
		if n == name {
			matches = append(matches, d)
		}
	}
	sort.Strings(matches)
	return matches, nil
}

// Name returns the content of the hwmon "name" file.
func Name(hwmonDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(hwmonDir, "name"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// ReadTemp reads a tempN_input file (e.g. "temp1_input") from the given
// hwmon directory and returns the value in degrees Celsius.
func ReadTemp(hwmonDir, inputName string) (float64, error) {
	path := filepath.Join(hwmonDir, inputName)
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("reading %s: %w", path, err)
	}
	millideg, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
	if err != nil {
		return 0, fmt.Errorf("parsing %s: %w", path, err)
	}
	return millideg / 1000, nil
}

// Reading pairs a sensor label (from tempN_label, or "tempN" if no label
// file exists) with its current temperature in Celsius.
type Reading struct {
	Label   string
	Celsius float64
}

// ReadAll enumerates every tempN_input file in the hwmon directory and
// returns each reading with its label. Entries that can't be read are
// skipped silently.
func ReadAll(hwmonDir string) ([]Reading, error) {
	inputs, err := filepath.Glob(filepath.Join(hwmonDir, "temp*_input"))
	if err != nil {
		return nil, err
	}
	sort.Slice(inputs, func(i, j int) bool {
		return tempIndex(inputs[i]) < tempIndex(inputs[j])
	})

	var out []Reading
	for _, inp := range inputs {
		base := filepath.Base(inp)
		c, err := ReadTemp(hwmonDir, base)
		if err != nil {
			continue
		}
		labelFile := strings.TrimSuffix(inp, "_input") + "_label"
		label := strings.TrimSuffix(base, "_input")
		if data, err := os.ReadFile(labelFile); err == nil {
			if s := strings.TrimSpace(string(data)); s != "" {
				label = s
			}
		}
		out = append(out, Reading{Label: label, Celsius: c})
	}
	return out, nil
}

// CountTempInputs returns the number of tempN_input files under hwmonDir.
func CountTempInputs(hwmonDir string) int {
	m, _ := filepath.Glob(filepath.Join(hwmonDir, "temp*_input"))
	return len(m)
}

// PCIAddress returns the PCI address (e.g. "0000:03:00.0") of the device
// that owns this hwmon, or "" if it isn't a PCI device.
func PCIAddress(hwmonDir string) string {
	abs, err := filepath.EvalSymlinks(filepath.Join(hwmonDir, "device"))
	if err != nil {
		return ""
	}
	// Walk up until we hit a segment that looks like a PCI address.
	for p := abs; p != "/" && p != "."; p = filepath.Dir(p) {
		base := filepath.Base(p)
		if looksLikePCIAddress(base) {
			return base
		}
	}
	return ""
}

func looksLikePCIAddress(s string) bool {
	// 0000:03:00.0 — four hex, colon, two hex, colon, two hex, dot, one hex.
	if len(s) != 12 {
		return false
	}
	return s[4] == ':' && s[7] == ':' && s[10] == '.' &&
		isHex(s[0:4]) && isHex(s[5:7]) && isHex(s[8:10]) && isHex(s[11:12])
}

func isHex(s string) bool {
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return len(s) > 0
}

// tempIndex extracts N from a path ending in "tempN_input" for numeric sorting.
func tempIndex(path string) int {
	base := filepath.Base(path)
	s := strings.TrimPrefix(base, "temp")
	s = strings.TrimSuffix(s, "_input")
	n, _ := strconv.Atoi(s)
	return n
}
