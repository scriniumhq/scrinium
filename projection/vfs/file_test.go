package vfs

import (
	"io"
	"io/fs"
	"testing"
	"time"

	fso "scrinium.dev/projection/internal/fsops"
	"scrinium.dev/testutil/projectionfx"
)

// fakeFsoFile is a minimal in-memory fsops.Handle for exercising rwFile's
// offset tracking and delegation without a real store handle.
type fakeFsoFile struct {
	buf    []byte
	closes int
	syncs  int
}

func (f *fakeFsoFile) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fs.ErrInvalid
	}
	if off >= int64(len(f.buf)) {
		return 0, io.EOF
	}
	n := copy(p, f.buf[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (f *fakeFsoFile) WriteAt(p []byte, off int64) (int, error) {
	if end := off + int64(len(p)); end > int64(len(f.buf)) {
		grown := make([]byte, end)
		copy(grown, f.buf)
		f.buf = grown
	}
	copy(f.buf[off:], p)
	return len(p), nil
}

func (f *fakeFsoFile) Close() error { f.closes++; return nil }
func (f *fakeFsoFile) Sync() error  { f.syncs++; return nil }

func (f *fakeFsoFile) Truncate(size int64) error {
	if size >= 0 && size < int64(len(f.buf)) {
		f.buf = f.buf[:size]
	}
	return nil
}

var _ fso.Handle = (*fakeFsoFile)(nil)

// --- readHandleFile ---

func TestReadHandleFile_RandomAccess(t *testing.T) {
	rh := projectionfx.NewReadHandle([]byte("0123456789")) // random access on by default
	f := &readHandleFile{rh: rh, name: "ro", size: 10, mtime: time.Unix(50, 0)}

	buf := make([]byte, 4)
	// Read is ReadAt-based; the offset advances across calls.
	if n, err := f.Read(buf); err != nil || string(buf[:n]) != "0123" {
		t.Fatalf("read 1 = %q, %v; want \"0123\", nil", buf[:n], err)
	}
	if n, err := f.Read(buf); err != nil || string(buf[:n]) != "4567" {
		t.Fatalf("read 2 = %q, %v; want \"4567\", nil", buf[:n], err)
	}
	// The final partial read defers EOF: 2 bytes with a nil error...
	if n, err := f.Read(buf); err != nil || string(buf[:n]) != "89" {
		t.Fatalf("read 3 = %q, %v; want \"89\", nil (deferred EOF)", buf[:n], err)
	}
	// ...then the next read reports EOF with no bytes.
	if n, err := f.Read(buf); n != 0 || err != io.EOF {
		t.Fatalf("read 4 = %d, %v; want 0, EOF", n, err)
	}

	// Seek rewinds; Read resumes from there.
	if off, err := f.Seek(0, io.SeekStart); off != 0 || err != nil {
		t.Fatalf("Seek(0,Start) = %d, %v; want 0, nil", off, err)
	}
	if n, err := f.Read(buf); err != nil || string(buf[:n]) != "0123" {
		t.Errorf("read after seek = %q, %v; want \"0123\", nil", buf[:n], err)
	}

	// Write is rejected; Stat reports a read-only mode.
	if _, err := f.Write([]byte("x")); err != fs.ErrPermission {
		t.Errorf("Write err = %v; want ErrPermission", err)
	}
	fi, err := f.Stat()
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Name() != "ro" || fi.Size() != 10 || fi.Mode().Perm() != 0o444 {
		t.Errorf("Stat = name %q size %d mode %v; want \"ro\" 10 0o444", fi.Name(), fi.Size(), fi.Mode())
	}

	// ReadAt is positioned and independent of the Seek cursor.
	if n, err := f.ReadAt(buf, 6); err != nil || string(buf[:n]) != "6789" {
		t.Errorf("ReadAt(6) = %q, %v; want \"6789\", nil", buf[:n], err)
	}
	if err := f.Sync(); err != nil {
		t.Errorf("Sync = %v; want nil", err)
	}
	if err := f.Close(); err != nil {
		t.Errorf("Close = %v; want nil", err)
	}
}

func TestReadHandleFile_StreamOnly(t *testing.T) {
	rh := projectionfx.NewReadHandle([]byte("hello"), projectionfx.WithStreamOnly())
	f := &readHandleFile{rh: rh, name: "stream", size: 5}

	buf := make([]byte, 3)
	// The first read at offset 0 streams.
	if n, err := f.Read(buf); err != nil || string(buf[:n]) != "hel" {
		t.Fatalf("stream read 1 = %q, %v; want \"hel\", nil", buf[:n], err)
	}
	// The cursor is now non-zero; a stream-only handle cannot seek back, so
	// the next read errors rather than silently losing data.
	if n, err := f.Read(buf); err == nil || n != 0 {
		t.Errorf("stream read 2 = %d, %v; want 0, error", n, err)
	}
	// ReadAt is unsupported on a stream-only handle.
	if n, err := f.ReadAt(buf, 0); err == nil || n != 0 {
		t.Errorf("stream ReadAt = %d, %v; want 0, error", n, err)
	}
}

// --- bytesFile ---

func TestBytesFile_Read(t *testing.T) {
	f := newBytesFile("data", []byte("0123456789"), time.Unix(100, 0))
	buf := make([]byte, 4)
	for _, want := range []string{"0123", "4567", "89"} {
		n, err := f.Read(buf)
		if err != nil || string(buf[:n]) != want {
			t.Fatalf("read = %q, %v; want %q, nil", buf[:n], err, want)
		}
	}
	if n, err := f.Read(buf); n != 0 || err != io.EOF {
		t.Errorf("read past end = %d, %v; want 0, EOF", n, err)
	}
}

func TestBytesFile_Seek(t *testing.T) {
	f := newBytesFile("data", []byte("0123456789"), time.Time{})
	buf := make([]byte, 4)

	if off, _ := f.Seek(2, io.SeekStart); off != 2 {
		t.Errorf("Seek(2,Start) = %d; want 2", off)
	}
	if n, _ := f.Read(buf); string(buf[:n]) != "2345" {
		t.Errorf("read after SeekStart = %q; want \"2345\"", buf[:n])
	}
	if off, _ := f.Seek(-2, io.SeekEnd); off != 8 {
		t.Errorf("Seek(-2,End) = %d; want 8", off)
	}
	if off, _ := f.Seek(-3, io.SeekCurrent); off != 5 {
		t.Errorf("Seek(-3,Current from 8) = %d; want 5", off)
	}
	// A negative resulting offset clamps to 0 and reports an error.
	if off, err := f.Seek(-100, io.SeekStart); off != 0 || err != fs.ErrInvalid {
		t.Errorf("Seek(-100,Start) = %d, %v; want 0, ErrInvalid", off, err)
	}
	// An unknown whence is rejected.
	if _, err := f.Seek(0, 99); err != fs.ErrInvalid {
		t.Errorf("Seek(_,99) err = %v; want ErrInvalid", err)
	}
}

func TestBytesFile_ReadAt(t *testing.T) {
	f := newBytesFile("data", []byte("0123456789"), time.Time{})
	buf := make([]byte, 4)

	// A full read mid-slice: exactly len(p) bytes, nil error.
	if n, err := f.ReadAt(buf, 2); err != nil || string(buf[:n]) != "2345" {
		t.Errorf("ReadAt(2) = %q, %v; want \"2345\", nil", buf[:n], err)
	}
	// A short read near the end: fewer than len(p) bytes with EOF.
	if n, err := f.ReadAt(buf, 8); n != 2 || err != io.EOF || string(buf[:n]) != "89" {
		t.Errorf("ReadAt(8) = %q (n=%d), %v; want \"89\" (2), EOF", buf[:n], n, err)
	}
	// Past the end: EOF, no bytes.
	if n, err := f.ReadAt(buf, 100); n != 0 || err != io.EOF {
		t.Errorf("ReadAt(100) = %d, %v; want 0, EOF", n, err)
	}
	// A negative offset is invalid.
	if n, err := f.ReadAt(buf, -1); n != 0 || err != fs.ErrInvalid {
		t.Errorf("ReadAt(-1) = %d, %v; want 0, ErrInvalid", n, err)
	}
}

func TestBytesFile_WritesRejectedAndMeta(t *testing.T) {
	f := newBytesFile("data", []byte("x"), time.Unix(7, 0))
	if _, err := f.Write([]byte("y")); err != fs.ErrPermission {
		t.Errorf("Write err = %v; want ErrPermission", err)
	}
	if _, err := f.WriteAt([]byte("y"), 0); err != fs.ErrPermission {
		t.Errorf("WriteAt err = %v; want ErrPermission", err)
	}
	fi, err := f.Stat()
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Name() != "data" || fi.Size() != 1 {
		t.Errorf("Stat = name %q size %d; want \"data\" 1", fi.Name(), fi.Size())
	}
	if err := f.Sync(); err != nil {
		t.Errorf("Sync = %v; want nil", err)
	}
}

// --- rwFile ---

func TestRWFile_ReadWriteOffset(t *testing.T) {
	bk := &fakeFsoFile{buf: []byte("0123456789")}
	f := wrapFile(bk, "/dir/file.txt", fso.FileInfo{Size: 10, ModTime: time.Unix(1, 0), Mode: 0o644})

	buf := make([]byte, 4)
	// Read advances the cursor via the backing ReadAt.
	if n, err := f.Read(buf); err != nil || string(buf[:n]) != "0123" {
		t.Fatalf("read = %q, %v; want \"0123\", nil", buf[:n], err)
	}
	// Write lands at the current cursor (4) via WriteAt.
	if n, err := f.Write([]byte("ABCD")); n != 4 || err != nil {
		t.Fatalf("write = %d, %v; want 4, nil", n, err)
	}
	if string(bk.buf) != "0123ABCD89" {
		t.Errorf("backing buf = %q; want \"0123ABCD89\"", bk.buf)
	}
	// Stat: name is the last path segment, mode preserved.
	fi, err := f.Stat()
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Name() != "file.txt" || fi.Mode().Perm() != 0o644 {
		t.Errorf("Stat = name %q mode %v; want \"file.txt\" 0o644", fi.Name(), fi.Mode())
	}
	// Sync delegates to the backing file.
	if err := f.Sync(); err != nil || bk.syncs != 1 {
		t.Errorf("Sync = %v, backing syncs = %d; want nil, 1", err, bk.syncs)
	}
	// Close is idempotent and closes the backing file exactly once.
	if err := f.Close(); err != nil {
		t.Errorf("Close 1 = %v; want nil", err)
	}
	if err := f.Close(); err != nil {
		t.Errorf("Close 2 = %v; want nil", err)
	}
	if bk.closes != 1 {
		t.Errorf("backing closed %d times; want 1 (Close must be idempotent)", bk.closes)
	}
}

func TestRWFile_WriteAtGrowsSize(t *testing.T) {
	bk := &fakeFsoFile{}
	f := wrapWriteFile(bk, "/new.txt") // a fresh file, size 0

	// A positioned write past the cached size grows it.
	if n, err := f.WriteAt([]byte("hello"), 10); n != 5 || err != nil {
		t.Fatalf("WriteAt = %d, %v; want 5, nil", n, err)
	}
	fi, _ := f.Stat()
	if fi.Size() != 15 {
		t.Errorf("size after WriteAt(off=10,len=5) = %d; want 15", fi.Size())
	}
	// ReadAt delegates straight through.
	buf := make([]byte, 5)
	if n, err := f.ReadAt(buf, 10); err != nil || string(buf[:n]) != "hello" {
		t.Errorf("ReadAt(10) = %q, %v; want \"hello\", nil", buf[:n], err)
	}
	if err := f.Close(); err != nil {
		t.Errorf("Close = %v; want nil", err)
	}
}

// --- blackHoleFile ---

func TestBlackHoleFile(t *testing.T) {
	f := NewBlackHoleFile("junk")

	// Reads always return EOF.
	if n, err := f.Read(make([]byte, 8)); n != 0 || err != io.EOF {
		t.Errorf("Read = %d, %v; want 0, EOF", n, err)
	}
	// Writes are discarded but report full acceptance.
	if n, err := f.Write([]byte("hello")); n != 5 || err != nil {
		t.Errorf("Write = %d, %v; want 5, nil", n, err)
	}
	if n, err := f.Write([]byte("xyz")); n != 3 || err != nil {
		t.Errorf("Write 2 = %d, %v; want 3, nil", n, err)
	}
	// Stat size reflects the total bytes written.
	fi, err := f.Stat()
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Name() != "junk" || fi.Size() != 8 {
		t.Errorf("Stat = name %q size %d; want \"junk\" 8", fi.Name(), fi.Size())
	}

	fa, ok := f.(FileAt)
	if !ok {
		t.Fatal("blackHoleFile does not implement FileAt")
	}
	// ReadAt is EOF; WriteAt discards but counts.
	if n, err := fa.ReadAt(make([]byte, 4), 0); n != 0 || err != io.EOF {
		t.Errorf("ReadAt = %d, %v; want 0, EOF", n, err)
	}
	if n, err := fa.WriteAt([]byte("abcd"), 100); n != 4 || err != nil {
		t.Errorf("WriteAt = %d, %v; want 4, nil", n, err)
	}
	if err := fa.Sync(); err != nil {
		t.Errorf("Sync = %v; want nil", err)
	}
	if err := f.Close(); err != nil {
		t.Errorf("Close = %v; want nil", err)
	}
}

func TestBlackHoleFile_Seek(t *testing.T) {
	f := NewBlackHoleFile("junk")
	if _, err := f.Write([]byte("abcde")); err != nil { // written = 5
		t.Fatalf("Write: %v", err)
	}

	// SeekStart returns the requested offset verbatim.
	if off, err := f.Seek(3, io.SeekStart); off != 3 || err != nil {
		t.Errorf("Seek(3,Start) = %d, %v; want 3, nil", off, err)
	}
	// SeekCurrent reports the bytes written so far.
	if off, err := f.Seek(0, io.SeekCurrent); off != 5 || err != nil {
		t.Errorf("Seek(_,Current) = %d, %v; want 5, nil", off, err)
	}
	// SeekEnd is written + offset.
	if off, err := f.Seek(2, io.SeekEnd); off != 7 || err != nil {
		t.Errorf("Seek(2,End) = %d, %v; want 7, nil", off, err)
	}
	// An unknown whence is rejected.
	if _, err := f.Seek(0, 99); err != fs.ErrInvalid {
		t.Errorf("Seek(_,99) err = %v; want ErrInvalid", err)
	}
}
