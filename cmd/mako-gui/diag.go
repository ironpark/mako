package main

import (
	"cmp"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime/debug"
	"runtime/metrics"
	"runtime/pprof"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/ironpark/ggfx"
	"github.com/ironpark/ggui"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gui"
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
		" footprint_mb gpu_images_mb gpu_queue_mb fps frame_ms_per_s flushes_per_s commands_per_s vertices_per_s"+
		" fps_for_animation fps_for_now fps_for_callback fps_for_posted fps_for_input partial_fps skipped_fps")
	samples := make([]metrics.Sample, len(diagSamples))
	for i, name := range diagSamples {
		samples[i].Name = name
	}
	var last diagReading
	var lastHeap, lastIncident time.Time
	go watchBursts(dir)
	for now := range time.Tick(diagEvery) {
		metrics.Read(samples)
		r := readDiag(samples, now)
		if !last.at.IsZero() {
			d := r.since(last)
			fmt.Fprintf(log, "%s %.1f %.1f %.1f %.1f %d %.2f %.2f %.2f %.1f %.1f %.1f %.1f %.1f %.1f %.0f %.0f %.1f %.1f %.1f %.1f %.1f %.1f %.1f\n", now.Format(time.RFC3339),
				mb(r.live), mb(r.goal), mb(r.total), mb(r.limit), r.goroutines, d.gcRate, d.gcShare, d.cores,
				mb(r.footprint), mb(r.gpuImages), mb(r.gpuQueue), d.fps, d.frameMS, d.flushes, d.commands, d.vertices,
				d.fpsFor[0], d.fpsFor[1], d.fpsFor[2], d.fpsFor[3], d.fpsFor[4], d.partialFPS, d.skippedFPS)
			if d.fps >= 1 {
				fmt.Fprintln(log, "# moving:", motionLine(ggui.ReadMotionReport()))
			}
			fmt.Fprintln(log, "# atlas:", r.atlas)
			fmt.Fprintln(log, "# terminals:", terminalLine(r.terms))
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
	framesFor                      [5]int64 // asked for by an animation, Now, a callback, posted work, input
	partial                        int64    // painted in part, around what moved
	skipped                        int64    // painted nothing, as nothing changed
	frameBusy                      time.Duration
	flushes, commands, vertices    int64
	atlas                          string // the GPU's images: see atlasLine
	terms                          gui.TerminalMemory
}

func readDiag(s []metrics.Sample, at time.Time) diagReading {
	var gpu ggfx.DebugInfo
	ggfx.ReadDebugInfo(&gpu)
	frames := ggui.ReadFrameStats()
	return diagReading{
		at: at, live: s[0].Value.Uint64(), goal: s[1].Value.Uint64(), total: s[2].Value.Uint64(),
		limit: s[3].Value.Uint64(), goroutines: s[4].Value.Uint64(), gcs: s[5].Value.Uint64(),
		gcCPU: s[6].Value.Float64(), userCPU: s[7].Value.Float64(), scavCPU: s[8].Value.Float64(),
		footprint: footprint(), gpuImages: uint64(gpu.TotalGPUImageMemoryUsageInBytes - gpu.ScreenBytes), gpuQueue: uint64(gpu.QueueBufferBytes),
		frames: frames.Frames, frameBusy: frames.Busy,
		framesFor: [5]int64{frames.ForAnimation, frames.ForTime, frames.ForCallback, frames.ForPosted, frames.ForInput},
		partial:   frames.Partial,
		skipped:   frames.Skipped,
		flushes:   gpu.Flushes, commands: gpu.Commands, vertices: gpu.Vertices,
		atlas: atlasLine(gpu),
		terms: gui.ReadTerminalMemory(),
	}
}

// terminalLine tells what the terminals followed hold in ghostty's pages:
// the memory they use, the address space they reserve, and how much of what
// they use is compressed scrollback.
func terminalLine(m gui.TerminalMemory) string {
	return fmt.Sprintf("%d, %.1f MB used of %.1f MB, %.1f MB of it compressed; %d pages, %d compressed, %d the most in one",
		m.Terminals, mb(m.Resident), mb(m.Virtual), mb(m.Compressed), m.Pages, m.CompressedPages, m.MostPages)
}

// atlasLine tells the GPU's images apart: each atlas page, largest first,
// its size, how much of it its images use and how many there are, "s" for
// one that serves as a source, then the images that have a GPU image of
// their own, the screens among them. A page grows as images come and lets
// go only once all of them have gone, so one that holds little of what it
// keeps shows here.
func atlasLine(gpu ggfx.DebugInfo) string {
	pages := slices.SortedFunc(slices.Values(gpu.AtlasPages), func(a, b ggfx.AtlasPage) int { return cmp.Compare(b.Bytes, a.Bytes) })
	var kept, used int64
	var b strings.Builder
	for _, p := range pages {
		kept += p.Bytes
		used += 4 * p.UsedPixels
	}
	fmt.Fprintf(&b, "%d pages %.1f MB, %.0f%% used;", len(pages), mb(uint64(kept)), percent(used, kept))
	for i, p := range pages {
		if i == 8 {
			fmt.Fprintf(&b, " and %d more", len(pages)-i)
			break
		}
		src := ""
		if p.Source {
			src = "s"
		}
		fmt.Fprintf(&b, " %dx%d%s %.0f%% %d", p.Width, p.Height, src, percent(4*p.UsedPixels, p.Bytes), p.Images)
	}
	fmt.Fprintf(&b, "; own %.1f MB in %d", mb(uint64(gpu.Isolated.Bytes)), gpu.Isolated.Images)
	return b.String()
}

func percent(part, whole int64) float64 {
	if whole == 0 {
		return 0
	}
	return 100 * float64(part) / float64(whole)
}

// diagRates is how the runtime went between two readings.
type diagRates struct {
	gcRate  float64 // collections a second
	gcShare float64 // of the CPU the program used, the collector's
	cores   float64 // the CPU the program used, in cores

	fps                         float64 // frames run a second
	frameMS                     float64 // milliseconds a second spent in them
	flushes, commands, vertices float64 // sent to the GPU, a second
	fpsFor                      [5]float64
	partialFPS                  float64 // frames painted in part, a second
	skippedFPS                  float64 // frames that painted nothing, a second
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
	out.partialFPS = float64(r.partial-prev.partial) / secs
	out.skippedFPS = float64(r.skipped-prev.skipped) / secs
	for i := range r.framesFor {
		out.fpsFor[i] = float64(r.framesFor[i]-prev.framesFor[i]) / secs
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

// motionLine writes what moved in the last frame, counted by type, and the
// frames painted whole that might have been painted in part, by why.
func motionLine(r ggui.MotionReport) string {
	counts := map[string]int{}
	for _, m := range r.Movers {
		counts[m]++
	}
	var parts []string
	for _, m := range slices.Sorted(maps.Keys(counts)) {
		parts = append(parts, fmt.Sprintf("%s×%d", m, counts[m]))
	}
	var whole []string
	for _, why := range slices.Sorted(maps.Keys(r.Refusals)) {
		whole = append(whole, fmt.Sprintf("%s=%d", why, r.Refusals[why]))
	}
	return strings.Join(parts, " ") + "; painted whole: " + strings.Join(whole, " ") + "; by: " + dirtiersLine(r.Dirtiers, mostDirtiers)
}

// mostDirtiers is how many of what made frames whole the stats name.
const mostDirtiers = 8

// dirtiersLine names the n sites that made the most frames whole, the most
// first, with their counts.
func dirtiersLine(by map[string]int64, n int) string {
	names := slices.SortedFunc(maps.Keys(by), func(a, b string) int {
		if c := cmp.Compare(by[b], by[a]); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	if len(names) > n {
		names = names[:n]
	}
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = fmt.Sprintf("%s=%d", strings.ReplaceAll(name, "github.com/ironpark/", ""), by[name])
	}
	return strings.Join(parts, " ")
}

// A burst is the CPU busy for a second or two, as a scroll's flick keeps
// it: the minute's average hides it, and the incident's threshold is far
// above it. watchBursts reads the CPU the process used each burstTick and,
// when a second of it kept burstCores busy, profiles the next burstCPU to
// burst-<time>.pb.gz, with what the frames did meanwhile beside it, at
// most once each burstGap, and not in the first burstAfter.
const (
	burstTick  = 250 * time.Millisecond
	burstCores = 0.35
	burstCPU   = 3 * time.Second
	burstGap   = 3 * time.Minute
	burstAfter = time.Minute // past the start, which keeps the CPU busy by itself
	burstsKept = 6
)

func watchBursts(dir string) {
	var window [4]time.Duration // the CPU used in each of the last ticks
	last, i := processCPU(), 0
	lastBurst := time.Now().Add(burstAfter - burstGap)
	for now := range time.Tick(burstTick) {
		used := processCPU()
		window[i%len(window)], last = used-last, used
		i++
		var sum time.Duration
		for _, d := range window {
			sum += d
		}
		if i < len(window) || sum.Seconds() < burstCores*burstTick.Seconds()*float64(len(window)) || now.Sub(lastBurst) < burstGap {
			continue
		}
		lastBurst = now
		keepBurst(dir, now)
		last, i = processCPU(), 0
	}
}

// processCPU is the CPU time the process has used, user and system.
func processCPU() time.Duration {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

// keepBurst profiles the CPU over burstCPU, and notes how the frames went.
func keepBurst(dir string, now time.Time) {
	name := filepath.Join(dir, "burst-"+now.Format("20060102-150405"))
	f, err := os.Create(name + ".pb.gz")
	if err != nil {
		return
	}
	before, motion := ggui.ReadFrameStats(), ggui.ReadMotionReport()
	// Not while another CPU profile runs, as an incident's may.
	if pprof.StartCPUProfile(f) != nil {
		f.Close()
		os.Remove(name + ".pb.gz")
		return
	}
	time.Sleep(burstCPU)
	pprof.StopCPUProfile()
	f.Close()
	after, motion2 := ggui.ReadFrameStats(), ggui.ReadMotionReport()
	secs := burstCPU.Seconds()
	var b strings.Builder
	fmt.Fprintf(&b, "frames %.1f/s, painted in part %.1f/s, skipped %.1f/s, busy %.0f ms/s\n",
		float64(after.Frames-before.Frames)/secs, float64(after.Partial-before.Partial)/secs,
		float64(after.Skipped-before.Skipped)/secs, float64(after.Busy-before.Busy)/1e6/secs)
	b.WriteString("painted whole, by why:")
	for _, why := range slices.Sorted(maps.Keys(motion2.Refusals)) {
		if n := motion2.Refusals[why] - motion.Refusals[why]; n > 0 {
			fmt.Fprintf(&b, " %s=%d", why, n)
		}
	}
	b.WriteString("\nby:")
	for _, who := range slices.Sorted(maps.Keys(motion2.Dirtiers)) {
		if n := motion2.Dirtiers[who] - motion.Dirtiers[who]; n > 0 {
			fmt.Fprintf(&b, "\n  %s=%d", who, n)
		}
	}
	b.WriteString("\n")
	os.WriteFile(name+".txt", []byte(b.String()), 0o600)
	prune(dir, "burst-*.pb.gz", burstsKept)
	prune(dir, "burst-*.txt", burstsKept)
}
