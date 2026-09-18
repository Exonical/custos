// Package units parses the spec-facing resource unit syntaxes of
// docs/workflows.md §Resource units: memory ("8Gi", "8G", "8192Mi",
// "512Ki", "1Ti") with Kubernetes-quantity semantics reimplemented
// locally (no apimachinery dependency), and walltime in both Go
// duration and Slurm formats.
package units

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// binaryScale maps binary suffixes to powers of 1024.
var binaryScale = map[string]int64{
	"KI": 1 << 10, "MI": 1 << 20, "GI": 1 << 30,
	"TI": 1 << 40, "PI": 1 << 50, "EI": 1 << 60,
}

// decimalScale maps decimal suffixes to powers of 1000.
var decimalScale = map[string]int64{
	"K": 1e3, "M": 1e6, "G": 1e9, "T": 1e12, "P": 1e15, "E": 1e18,
}

// ParseMemoryMiB parses a memory quantity into mebibytes. Binary
// suffixes (Ki/Mi/Gi/Ti/Pi/Ei) scale by 1024, decimal suffixes
// (K/M/G/T/P/E) by 1000; a bare integer is MiB. The result rounds up so
// a sub-MiB decimal quantity never allocates zero.
func ParseMemoryMiB(v string) (int64, error) {
	s := strings.TrimSpace(v)
	if s == "" {
		return 0, fmt.Errorf("empty memory value")
	}
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
		i++
	}
	num, suffix := s[:i], strings.ToUpper(s[i:])
	if num == "" {
		return 0, fmt.Errorf("invalid memory value %q", v)
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("invalid memory value %q", v)
	}
	var scale int64
	if suffix == "" {
		scale = 1 << 20 // bare number is MiB
	} else if b, ok := binaryScale[suffix]; ok {
		scale = b
	} else if d, ok := decimalScale[suffix]; ok {
		scale = d
	} else {
		return 0, fmt.Errorf("invalid memory suffix in %q", v)
	}
	bytes := f * float64(scale)
	mib := bytes / float64(int64(1)<<20)
	if mib >= float64(math.MaxInt64) {
		return 0, fmt.Errorf("memory value %q overflows int64", v)
	}
	out := int64(mib)
	if float64(out) < mib {
		out++ // round up: never under-allocate
	}
	return out, nil
}

// ParseWalltime accepts Go durations ("30m", "4h30m") and Slurm
// walltime formats ("M", "M:S", "H:M:S", "D-H", "D-H:M", "D-H:M:S").
func ParseWalltime(v string) (time.Duration, error) {
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return d, nil
	}
	if d, ok := ParseSlurmTime(v); ok {
		return d, nil
	}
	return 0, fmt.Errorf("invalid walltime %q", v)
}

// ParseSlurmTime parses Slurm walltime forms only: M, M:S, H:M:S,
// D-H, D-H:M, D-H:M:S.
func ParseSlurmTime(v string) (time.Duration, bool) {
	var days, hours, mins, secs int64
	hasDays := strings.Contains(v, "-")
	if hasDays {
		d, rest, _ := strings.Cut(v, "-")
		var err error
		if days, err = strconv.ParseInt(d, 10, 64); err != nil {
			return 0, false
		}
		v = rest
	}
	parts := strings.Split(v, ":")
	n := len(parts)
	if n > 3 {
		return 0, false
	}
	vals := make([]int64, n)
	for i, p := range parts {
		if p == "" {
			return 0, false
		}
		x, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return 0, false
		}
		vals[i] = x
	}
	switch {
	case hasDays:
		// D-H, D-H:M, D-H:M:S — after the dash the first part is hours.
		switch n {
		case 1:
			hours = vals[0]
		case 2:
			hours, mins = vals[0], vals[1]
		case 3:
			hours, mins, secs = vals[0], vals[1], vals[2]
		}
	case n == 1: // M
		mins = vals[0]
	case n == 2: // M:S
		mins, secs = vals[0], vals[1]
	default: // H:M:S
		hours, mins, secs = vals[0], vals[1], vals[2]
	}
	// Minutes/seconds must stay below 60 only when a higher-order field
	// follows them; bare "90:30" (M:S) and "1-25" (D-H, hours unbounded
	// past 24 is rejected below) are legal Slurm forms.
	if secs >= 60 || ((n >= 3 || (hasDays && n >= 2)) && mins >= 60) ||
		(hasDays && hours >= 24) {
		return 0, false
	}
	return time.Duration(days)*24*time.Hour +
		time.Duration(hours)*time.Hour +
		time.Duration(mins)*time.Minute +
		time.Duration(secs)*time.Second, true
}
