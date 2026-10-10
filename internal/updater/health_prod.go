//go:build !exmondev

package updater

// neverHealthy is the test switch from health_dev.go; a production build always
// reports health normally.
func neverHealthy() bool { return false }
