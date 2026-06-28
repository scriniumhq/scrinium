package driver

import "context"

// DriverNamer is the optional capability of a Driver to report a short,
// stable name for the backend it adapts ("localfs", "s3", …). Discovered by
// assertion — drv.(DriverNamer). The name is display-only diagnostics and
// carries no behavioural meaning; a driver that does not implement it is
// reported as unknown.
type DriverNamer interface {
	DriverName() string
}

// CapacityReporter is the optional capability of a Driver to report the
// physical capacity of the volume backing its storage. Discovered by
// assertion — drv.(CapacityReporter). Local-filesystem drivers implement it
// via statfs; object stores generally cannot and do not, which leaves the
// byte fields of domain.StorageInfo at the -1 "unavailable" sentinel.
type CapacityReporter interface {
	// DiskUsage reports the total and available bytes of the underlying
	// volume. Both are non-negative on success; on error the caller treats
	// capacity as unavailable rather than failing.
	DiskUsage(ctx context.Context) (total, available int64, err error)
}
