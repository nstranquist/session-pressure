package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"

	"github.com/nstranquist/session-pressure/sessionpressure/gocachelru"
)

func main() {
	maxRaw := flag.String("max", "16GiB", "maximum on-disk cache size")
	dir := flag.String("dir", "", "cache directory (default: ~/Library/Caches/go-build-capped)")
	flag.Parse()
	maxBytes, err := parseSize(*maxRaw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gocache-lru: %v\n", err)
		os.Exit(2)
	}
	cacheDir := *dir
	if cacheDir == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			fmt.Fprintf(os.Stderr, "gocache-lru: %v\n", homeErr)
			os.Exit(1)
		}
		cacheDir = filepath.Join(home, "Library", "Caches", "go-build-capped")
	}
	if err := gocachelru.Serve(os.Stdin, os.Stdout, cacheDir, maxBytes); err != nil {
		fmt.Fprintf(os.Stderr, "gocache-lru: %v\n", err)
		os.Exit(1)
	}
}

func parseSize(raw string) (int64, error) {
	value := raw
	factor := int64(1)
	switch {
	case len(raw) >= 3 && (raw[len(raw)-3:] == "GiB" || raw[len(raw)-3:] == "gib"):
		value = raw[:len(raw)-3]
		factor = 1 << 30
	case len(raw) >= 3 && (raw[len(raw)-3:] == "MiB" || raw[len(raw)-3:] == "mib"):
		value = raw[:len(raw)-3]
		factor = 1 << 20
	case len(raw) >= 2 && (raw[len(raw)-2:] == "GB" || raw[len(raw)-2:] == "gb"):
		value = raw[:len(raw)-2]
		factor = 1_000_000_000
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n <= 0 || n > math.MaxInt64/factor {
		return 0, fmt.Errorf("invalid size %q", raw)
	}
	return n * factor, nil
}
