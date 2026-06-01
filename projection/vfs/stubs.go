package vfs

import (
	"io/fs"
	"os"
)

// dirFileStub supplies the byte-I/O methods every directory file
// rejects identically: Read and Seek are invalid on a directory,
// Write is denied (the VFS dir surface is read-only). Embed it in a
// directory file type to inherit all three; the type still provides
// its own Readdir, Stat, and Close.
type dirFileStub struct{}

func (dirFileStub) Read([]byte) (int, error)       { return 0, fs.ErrInvalid }
func (dirFileStub) Write([]byte) (int, error)      { return 0, fs.ErrPermission }
func (dirFileStub) Seek(int64, int) (int64, error) { return 0, fs.ErrInvalid }

// nonDirStub supplies the Readdir method every regular (non-directory)
// file rejects identically. Embed it in a file type to inherit it; the
// type still provides its own Read/Write/Seek/Stat/Close.
type nonDirStub struct{}

func (nonDirStub) Readdir(int) ([]os.FileInfo, error) { return nil, fs.ErrInvalid }
