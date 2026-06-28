//go:build sqlite_cgo

package sqlite

// typedBusy is a no-op under the cgo (mattn) build: contention detection there
// stays on the message-text fallback (substringBusy), which the busy
// integration test exercises under this driver too. If message matching ever
// proves insufficient on mattn, replace this with an errors.As against
// *github.com/mattn/go-sqlite3.Error and a check on .Code against
// sqlite3.ErrBusy / sqlite3.ErrLocked.
func typedBusy(error) bool { return false }
