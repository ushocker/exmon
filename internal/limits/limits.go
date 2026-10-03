// Package limits holds the small checks every config section uses to keep numbers
// in a sensible range, so a typo can't make exmon exhaust the machine (for example
// a million path-trace hops or a billion rotated log files).
package limits

import "fmt"

// Range returns an error if v is outside lo..hi. name is the setting as it appears
// in the config file, e.g. "log.maxFiles".
func Range(name string, v, lo, hi int) error {
	if v < lo || v > hi {
		return fmt.Errorf("%s %d is out of range %d-%d", name, v, lo, hi)
	}
	return nil
}

// Count returns an error if a list has more than max entries.
func Count(name string, n, max int) error {
	if n > max {
		return fmt.Errorf("%s has %d entries; at most %d are allowed", name, n, max)
	}
	return nil
}

// First returns the first non-nil error, so a Validate method can list its checks
// in one call: return limits.First(limits.Range(...), limits.Range(...)).
func First(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
