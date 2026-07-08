package st

import (
	"strconv"
	"strings"
)

// ParseTimeMs converts a time literal like "5s", "100ms", "1h30m", "2d4h"
// to milliseconds. Units scanned left-to-right; unknown units count as ms.
// Exported so peer packages (LAD) can parse the same IEC time literal form.
func ParseTimeMs(raw string) int {
	raw = strings.TrimSpace(raw)
	total := 0
	i := 0
	for i < len(raw) {
		j := i
		for j < len(raw) && (raw[j] >= '0' && raw[j] <= '9') {
			j++
		}
		if j == i {
			break
		}
		num, _ := strconv.Atoi(raw[i:j])
		k := j
		for k < len(raw) && !(raw[k] >= '0' && raw[k] <= '9') {
			k++
		}
		switch strings.ToLower(raw[j:k]) {
		case "ms":
			total += num
		case "s":
			total += num * 1000
		case "m":
			total += num * 60000
		case "h":
			total += num * 3600000
		case "d":
			total += num * 86400000
		default:
			total += num
		}
		i = k
	}
	return total
}
