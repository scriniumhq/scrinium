package sqlite

// DriverName implements index.DriverNamer: the human-facing name of the
// SQLite backend, distinguishing the cgo and pure-Go builds. driverLabel is
// defined by the build-tagged driver_{cgo,purego}.go alongside driverName.
func (i *Index) DriverName() string { return driverLabel }
