package localfs

// DriverName implements driver.DriverNamer: the stable backend name for a
// local-filesystem store.
func (d *Driver) DriverName() string { return "localfs" }
