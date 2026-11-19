package bridge

import "syscall"

// errNotDir is the error of reading a directory listing from a file.
func errNotDir() error { return syscall.ENOTDIR }
