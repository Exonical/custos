package units

import (
	"testing"
	"time"
)

func TestParseMemoryMiB(t *testing.T) {
	cases := map[string]int64{
		"8192Mi": 8192, "8192M": 7813, "8Gi": 8192, "8G": 7630,
		"512Ki": 1, "1Ti": 1048576, "512": 512, "1T": 953675,
		"2048Ki": 2, "1.5Gi": 1536,
	}
	for in, want := range cases {
		got, err := ParseMemoryMiB(in)
		if err != nil || got != want {
			t.Errorf("%q: got %d err %v, want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "Gi", "-1Gi", "8XB", "abc", "8 Gi"} {
		if _, err := ParseMemoryMiB(bad); err == nil {
			t.Errorf("%q should not parse", bad)
		}
	}
}

func TestParseWalltime(t *testing.T) {
	cases := map[string]time.Duration{
		"30m": 30 * time.Minute, "4h30m": 4*time.Hour + 30*time.Minute,
		"90": 90 * time.Minute, "90:30": 90*time.Minute + 30*time.Second,
		"4:00:00": 4 * time.Hour, "1-12": 36 * time.Hour,
		"2-06:30":    54*time.Hour + 30*time.Minute,
		"1-02:03:04": 24*time.Hour + 2*time.Hour + 3*time.Minute + 4*time.Second,
	}
	for in, want := range cases {
		got, err := ParseWalltime(in)
		if err != nil || got != want {
			t.Errorf("%q: got %v err %v, want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "x", "1:90", "1-99:99", "0s", "-5m"} {
		if _, err := ParseWalltime(bad); err == nil {
			t.Errorf("%q should not parse", bad)
		}
	}
}

func FuzzParseMemoryMiB(f *testing.F) {
	for _, s := range []string{"8Gi", "8G", "8192Mi", "512Ki", "1Ti",
		"", "x", "999999999999999999Ei", "0.0001Ki", "1.5.2Gi"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		v, err := ParseMemoryMiB(s)
		if err == nil && v < 0 {
			t.Fatalf("negative: %q -> %d", s, v)
		}
	})
}

func FuzzParseWalltime(f *testing.F) {
	for _, s := range []string{"30m", "1-12:00:00", "4:00:00", "90:30",
		"", "x", "1:90", "999999-99:99:99", "1-2-3"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if d, err := ParseWalltime(s); err == nil && d <= 0 {
			t.Fatalf("non-positive: %q -> %v", s, d)
		}
	})
}
