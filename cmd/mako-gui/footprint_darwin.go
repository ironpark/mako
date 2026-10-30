package main

import (
	"encoding/binary"
	"os"
	"os/exec"
	"strconv"
	"unsafe"

	"github.com/ebitengine/purego"
)

// procPIDRusage is libproc's proc_pid_rusage, nil when it cannot be found.
var procPIDRusage func(pid int32, flavor int32, buffer unsafe.Pointer) int32

func init() {
	lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_GLOBAL|purego.RTLD_NOW)
	if err != nil {
		return
	}
	if _, err := purego.Dlsym(lib, "proc_pid_rusage"); err != nil {
		return
	}
	purego.RegisterLibFunc(&procPIDRusage, lib, "proc_pid_rusage")
}

// footprint is the app's physical footprint in bytes, what Activity
// Monitor shows as its memory: on Apple silicon the GPU's memory for it
// too, its textures and buffers, which the Go runtime does not see. 0
// when it cannot be read.
func footprint() uint64 {
	if procPIDRusage == nil {
		return 0
	}
	// rusage_info_v2: a 16-byte UUID, then 64-bit counts, the eighth of
	// which is ri_phys_footprint.
	var buf [256]byte
	if procPIDRusage(int32(os.Getpid()), 2, unsafe.Pointer(&buf[0])) != 0 {
		return 0
	}
	return binary.LittleEndian.Uint64(buf[16+7*8:])
}

// footprintReport writes footprint(1)'s account of the app's memory to
// path: by kind, the GPU's (IOAccelerator) apart from the heap's.
func footprintReport(path string) {
	out, err := exec.Command("/usr/bin/footprint", "-p", strconv.Itoa(os.Getpid())).CombinedOutput()
	if err != nil && len(out) == 0 {
		return
	}
	os.WriteFile(path, out, 0o600)
}
