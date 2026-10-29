//go:build !darwin

package main

// relieveMalloc leaves the C allocator be: only macOS's is asked here.
func relieveMalloc() {}
