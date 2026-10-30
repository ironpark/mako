package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestAnIncidentIsTheCollectorBusyOrTheCoresKept(t *testing.T) {
	at := time.Unix(0, 0)
	prev := diagReading{at: at}
	busy := diagReading{at: at.Add(time.Minute), gcs: 41 * 60, gcCPU: 0.6 * 7 * 60, userCPU: 0.4 * 7 * 60}
	if d := busy.since(prev); !d.troubled() || d.gcRate != 41 || d.cores < 6.9 {
		t.Fatalf("the collector at 41 a second on 7 cores is no incident: %+v", d)
	}
	calm := diagReading{at: at.Add(time.Minute), gcs: 60, gcCPU: 1, userCPU: 20, frames: 120}
	if d := calm.since(prev); d.troubled() || d.fps != 2 {
		t.Fatalf("a calm minute is an incident: %+v", d)
	}
}

func TestPruneKeepsTheNewest(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"heap-20261030-100000.pb.gz", "heap-20261030-110000.pb.gz", "heap-20261030-120000.pb.gz"} {
		os.WriteFile(filepath.Join(dir, n), nil, 0o600)
	}
	prune(dir, "heap-*.pb.gz", 2)
	left, _ := filepath.Glob(filepath.Join(dir, "heap-*"))
	if len(left) != 2 || filepath.Base(left[0]) != "heap-20261030-110000.pb.gz" {
		t.Fatal(left)
	}
}

func TestTheFootprintIsRead(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS only")
	}
	if f := footprint(); f < 1<<20 {
		t.Fatalf("footprint %d bytes", f)
	}
	path := filepath.Join(t.TempDir(), "footprint.txt")
	footprintReport(path)
	if b, err := os.ReadFile(path); err != nil || len(b) == 0 {
		t.Fatal("no footprint report", err)
	}
}
