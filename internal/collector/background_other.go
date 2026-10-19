//go:build !linux

package collector

func detachedProcessAlive(p processIdentity) bool {
	return pidAlive(p.PID)
}
