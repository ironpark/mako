//go:build !darwin

package main

func footprint() uint64 { return 0 }

func footprintReport(string) {}
