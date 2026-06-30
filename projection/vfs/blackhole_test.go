package vfs

import (
	"io"
	"io/fs"
	"testing"
)

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
