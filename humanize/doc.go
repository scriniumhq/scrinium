// Package humanize formats numeric values for display in
// user-facing output (stats endpoints, CLI tools, web UIs).
//
// Currently scoped to byte counts (Bytes, BytesSigned). Other
// unit families (durations, counts) might join later if needs
// warrant; for now the package stays small and focused on a
// single rendering convention so every Scrinium-backed surface
// produces identical output for the same input — a value shown
// as "1.42 MiB" in one place reads "1.42 MiB" in another, not
// "1.4 MiB" or "1.42M".
//
// The formatting follows IEC binary units (KiB = 1024 B, MiB =
// 1024 KiB, ...) with one decimal place above raw bytes.
package humanize
