//go:build !windows

package collector

import "time"

// setBirth does nothing: on macOS, os.Chtimes also moves the birth time back, and Linux does not use it.
func setBirth(string, time.Time) error { return nil }
