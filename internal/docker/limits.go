package docker

import (
	"fmt"
	"math"
	"time"
)

const (
	defaultLogLimit = 1 << 20
	maxLogLimit     = 16 << 20
	maxBuildPIDs    = 1024
	maxRuntimePIDs  = 4096
)

// MaxPIDs returns the default maximum PID limit for runtime containers.
func MaxPIDs() int64 {
	return maxRuntimePIDs
}

// Limits are the bounded settings applied to one build.
type Limits struct {
	MemoryBytes int64
	CPUs        float64
	Timeout     time.Duration
	LogBytes    int64
	PIDs        int64
}

// NormalizeLimits validates and fills safe build limits.
func NormalizeLimits(limits Limits) (Limits, error) {
	if limits.MemoryBytes <= 0 {
		return Limits{}, fmt.Errorf("build memory limit must be positive")
	}
	if limits.CPUs <= 0 || math.IsNaN(limits.CPUs) || math.IsInf(limits.CPUs, 0) {
		return Limits{}, fmt.Errorf("build CPU limit must be positive and finite")
	}
	if limits.Timeout <= 0 {
		return Limits{}, fmt.Errorf("build timeout must be positive")
	}
	if limits.LogBytes == 0 {
		limits.LogBytes = defaultLogLimit
	}
	if limits.LogBytes < 0 || limits.LogBytes > maxLogLimit {
		return Limits{}, fmt.Errorf("build log limit must be between 1 and %d bytes", maxLogLimit)
	}
	if limits.PIDs == 0 {
		limits.PIDs = maxBuildPIDs
	}
	if limits.PIDs < 1 || limits.PIDs > maxBuildPIDs {
		return Limits{}, fmt.Errorf("build PID limit must be between 1 and %d", maxBuildPIDs)
	}
	return limits, nil
}

const cpuPeriod int64 = 100000

func cpuQuota(cpus float64) int64 {
	quota := int64(cpus * float64(cpuPeriod))
	if quota < 1 {
		return 1
	}
	return quota
}
