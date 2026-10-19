package collector

import (
	"os"
	"strconv"
	"strings"
)

func detachedProcessAlive(p processIdentity) bool {
	if p.PID <= 0 || p.StartTicks == 0 || p.BootID == "" {
		return false
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil || strings.TrimSpace(string(boot)) != p.BootID {
		return false
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(p.PID) + "/stat")
	return err == nil && matchesProcessStat(string(stat), p.StartTicks)
}

func matchesProcessStat(stat string, startTicks uint64) bool {
	// comm may contain spaces and parentheses. Fields after its final ')' start at state.
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return false
	}
	fields := strings.Fields(stat[end+1:])
	if len(fields) < 20 || fields[0] == "Z" || fields[0] == "X" {
		return false
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	return err == nil && ticks == startTicks
}
