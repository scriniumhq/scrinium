package index

// DriverNamer is the optional capability of a StoreIndex to report a short,
// stable name for its backing database driver ("sqlite (modernc)", …).
// Discovered by assertion — idx.(DriverNamer). The name is display-only and
// carries no behavioural meaning; it mirrors driver.DriverNamer on the
// storage side.
type DriverNamer interface {
	DriverName() string
}
