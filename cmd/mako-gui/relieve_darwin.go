package main

import "github.com/ebitengine/purego"

// mallocPressureRelief is libmalloc's malloc_zone_pressure_relief, nil
// when it cannot be found.
var mallocPressureRelief func(zone uintptr, goal uintptr) uintptr

func init() {
	lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_GLOBAL|purego.RTLD_NOW)
	if err != nil {
		return
	}
	if _, err := purego.Dlsym(lib, "malloc_zone_pressure_relief"); err != nil {
		return
	}
	purego.RegisterLibFunc(&mallocPressureRelief, lib, "malloc_zone_pressure_relief")
}

// relieveMalloc returns the free pages of every malloc zone to the system.
func relieveMalloc() {
	if mallocPressureRelief != nil {
		mallocPressureRelief(0, 0) // every zone, as much as it can
	}
}
