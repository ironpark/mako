package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"runtime/metrics"
	"runtime/pprof"
	"slices"
	"time"

	"github.com/ironpark/ggfx"
	"github.com/ironpark/ggui"
	"github.com/ironpark/mako/internal/config"
)

// The app keeps, in diag in the configuration directory, what a look at
// its memory, its CPU or its GPU after the fact needs:
//
//   - stats.log: a line a minute of the Go runtime's numbers, the frames
//     the window ran and what they sent the GPU, and the app's footprint,
//     the GPU's memory for it included;
//   - every half hour, the heap's profile and footprint(1)'s account of
//     the app's memory by kind, the GPU's apart;
//   - an incident, when the collector takes much of the CPU, as it did
//     once for hours on end, or the app keeps several cores busy: the
//     CPU's profile, the heap's, the goroutines' and the footprint's,
//     then and there.
//
// MAKO_DIAG=off leaves it all out.

const (
	diagEvery      = time.Minute      // a line of stats.log
	heapEvery      = 30 * time.Minute // a heap profile
	heapsKept      = 16               // eight hours of them
	incidentsKept  = 5
	incidentGap    = time.Hour        // between one incident's profiles and the next's
	incidentCPU    = 10 * time.Second // the CPU profile an incident takes
	statsLogMax    = 4 << 20
	gcShareTrouble = .3 // of the CPU used, the collector's, over a minute
	gcRateTrouble  = 5  // collections a second, over a minute
	coresTrouble   = 3  // cores kept busy, over a minute
)

// diagSamples are the runtime's numbers stats.log keeps, in its order.
var diagSamples = []string{
	"/gc/heap/live:bytes",
	"/gc/heap/goal:bytes",
	"/memory/classes/total:bytes",
	"/gc/gomemlimit:bytes",
	"/sched/goroutines:goroutines",
	"/gc/cycles/total:gc-cycles",
	"/cpu/classes/gc/total:cpu-seconds",
	"/cpu/classes/user:cpu-seconds",
	"/cpu/classes/scavenge/total:cpu-seconds",
}

// diagnose keeps the app's diagnostics for as long as it runs.
func diagnose() {
	if os.Getenv("MAKO_DIAG") == "off" {
		return
	}
	root, err := config.Dir()
	if err != nil {
		return
	}
	dir := filepath.Join(root, "diag")
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	log := openStatsLog(filepath.Join(dir, "stats.log"))
	if log == nil {
		return
	}
	fmt.Fprintf(log, "# mako-gui %s, pid %d, started %s\n", revision(), os.Getpid(), time.Now().Format(time.RFC3339))
	fmt.Fprintln(log, "# time live_mb goal_mb go_mb limit_mb goroutines gcs_per_s gc_cpu_share cpu_cores"+
		" footprint_mb gpu_images_mb gpu_queue_mb fps frame_ms_per_s flushes_per_s commands_per_s vertices_per_s")
	samples := make([]metrics.Sample, len(diagSamples))
	for i, name := range diagSamples {
		samples[i].Name = name
	}
	var last diagReading
	var lastHeap, lastIncident time.Time
	for now := range time.Tick(diagEvery) {
		metrics.Read(samples)
		r := readDiag(samples, now)
		if !last.at.IsZero() {
			d := r.since(last)
			fmt.Fprintf(log, "%s %.1f %.1f %.1f %.1f %d %.2f %.2f %.2f %.1f %.1f %.1f %.1f %.1f %.1f %.0f %.0f\n", now.Format(time.RFC3339),
				mb(r.live), mb(r.goal), mb(r.total), mb(r.limit), r.goroutines, d.gcRate, d.gcShare, d.cores,
				mb(r.footprint), mb(r.gpuImages), mb(r.gpuQueue), d.fps, d.frameMS, d.flushes, d.commands, d.vertices)
			if d.troubled() && now.Sub(lastIncident) >= incidentGap {
				lastIncident = now
				fmt.Fprintf(log, "# incident: %.1f collections a second, %.0f%% of the CPU used, %.1f cores, %.1f frames a second\n",
					d.gcRate, 100*d.gcShare, d.cores, d.fps)
				go keepIncident(dir, now)
			}
		}
		last = r
		if now.Sub(lastHeap) >= heapEvery {
			lastHeap = now
			keepHeap(dir, now)
		}
	}
}

// openStatsLog opens stats.log to add to, started afresh once it is large.
func openStatsLog(path string) *os.File {
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if info, err := os.Stat(path); err == nil && info.Size() > statsLogMax {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil
	}
	return f
}

// diagReading is the runtime's numbers at a time.
type diagReading struct {
	at                       time.Time
	live, goal, total, limit uint64
	goroutines               uint64
	gcs                      uint64
	gcCPU, userCPU, scavCPU  float64

	footprint, gpuImages, gpuQueue uint64
	frames                         int64
	frameBusy                      time.Duration
	flushes, commands, vertices    int64
}

func readDiag(s []metrics.Sample, at time.Time) diagReading {
	var gpu ggfx.DebugInfo
	ggfx.ReadDebugInfo(&gpu)
	frames := ggui.ReadFrameStats()
	return diagReading{
		at: at, live: s[0].Value.Uint64(), goal: s[1].Value.Uint64(), total: s[2].Value.Uint64(),
		limit: s[3].Value.Uint64(), goroutines: s[4].Value.Uint64(), gcs: s[5].Value.Uint64(),
		gcCPU: s[6].Value.Float64(), userCPU: s[7].Value.Float64(), scavCPU: s[8].Value.Float64(),
		footprint: footprint(), gpuImages: uint64(gpu.TotalGPUImageMemoryUsageInBytes), gpuQueue: uint64(gpu.QueueBufferBytes),
		frames: frames.Frames, frameBusy: frames.Busy,
		flushes: gpu.Flushes, commands: gpu.Commands, vertices: gpu.Vertices,
	}
}

// diagRates is how the runtime went between two readings.
type diagRates struct {
	gcRate  float64 // collections a second
	gcShare float64 // of the CPU the program used, the collector's
	cores   float64 // the CPU the program used, in cores

	fps                         float64 // frames run a second
	frameMS                     float64 // milliseconds a second spent in them
	flushes, commands, vertices float64 // sent to the GPU, a second
}

func (r diagReading) since(prev diagReading) diagRates {
	secs := r.at.Sub(prev.at).Seconds()
	if secs <= 0 {
		return diagRates{}
	}
	gc := r.gcCPU - prev.gcCPU
	used := gc + r.userCPU - prev.userCPU + r.scavCPU - prev.scavCPU
	out := diagRates{
		gcRate: float64(r.gcs-prev.gcs) / secs, cores: used / secs,
		fps: float64(r.frames-prev.frames) / secs, frameMS: float64((r.frameBusy - prev.frameBusy).Milliseconds()) / secs,
		flushes: float64(r.flushes-prev.flushes) / secs, commands: float64(r.commands-prev.commands) / secs,
		vertices: float64(r.vertices-prev.vertices) / secs,
	}
	if used > 0 {
		out.gcShare = gc / used
	}
	return out
}

// troubled is whether the collector ran as it did for hours once, much of
// the CPU and many times a second, or the app kept several cores busy.
func (d diagRates) troubled() bool {
	return d.gcShare >= gcShareTrouble && d.gcRate >= gcRateTrouble || d.cores >= coresTrouble
}

func mb(b uint64) float64 { return float64(b) / (1 << 20) }

// keepHeap writes the heap's profile to heap-<time>.pb.gz and the
// footprint's account to footprint-<time>.txt, keeping the newest
// heapsKept of each.
func keepHeap(dir string, now time.Time) {
	stamp := now.Format("20060102-150405")
	writeProfile(filepath.Join(dir, "heap-"+stamp+".pb.gz"), "heap", 0)
	footprintReport(filepath.Join(dir, "footprint-"+stamp+".txt"))
	prune(dir, "heap-*.pb.gz", heapsKept)
	prune(dir, "footprint-*.txt", heapsKept)
}

// keepIncident writes, to incident-<time>, the CPU's profile over
// incidentCPU, then the heap's, the goroutines' and the runtime's own
// numbers, keeping the newest incidentsKept incidents.
func keepIncident(dir string, now time.Time) {
	at := filepath.Join(dir, "incident-"+now.Format("20060102-150405"))
	if os.MkdirAll(at, 0o700) != nil {
		return
	}
	prune(dir, "incident-*", incidentsKept)
	// Not while another CPU profile runs, as one from pprof's server may.
	if f, err := os.Create(filepath.Join(at, "cpu.pb.gz")); err == nil {
		if pprof.StartCPUProfile(f) == nil {
			time.Sleep(incidentCPU)
			pprof.StopCPUProfile()
		}
		f.Close()
	}
	writeProfile(filepath.Join(at, "heap.pb.gz"), "heap", 0)
	writeProfile(filepath.Join(at, "allocs.pb.gz"), "allocs", 0)
	writeProfile(filepath.Join(at, "goroutines.txt"), "goroutine", 1)
	footprintReport(filepath.Join(at, "footprint.txt"))
	if f, err := os.Create(filepath.Join(at, "gcstats.txt")); err == nil {
		var s debug.GCStats
		s.PauseQuantiles = make([]time.Duration, 5)
		debug.ReadGCStats(&s)
		fmt.Fprintf(f, "collections %d\npause total %s\npause quantiles %v\nmemory limit %d\n",
			s.NumGC, s.PauseTotal, s.PauseQuantiles, debug.SetMemoryLimit(-1))
		f.Close()
	}
}

func writeProfile(path, name string, debugLevel int) {
	p := pprof.Lookup(name)
	if p == nil {
		return
	}
	f, err := os.Create(path)
	if err != nil {
		return
	}
	defer f.Close()
	p.WriteTo(f, debugLevel)
}

// prune removes all but the newest keep of what pattern matches in dir;
// the names sort by the time in them.
func prune(dir, pattern string, keep int) {
	old, _ := filepath.Glob(filepath.Join(dir, pattern))
	slices.Sort(old)
	for _, p := range old[:max(len(old)-keep, 0)] {
		os.RemoveAll(p)
	}
}
