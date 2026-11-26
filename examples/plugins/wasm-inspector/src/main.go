//go:build wasip1 && wasm

// wasm-inspector is a pure-Go WebAssembly demo plugin for Mako (Revision 49).
// Build with:
//
//	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -ldflags="-s -w" -o plugin.wasm ./src
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unsafe"
)

// Mako Core Wasm ABI host imports (module "mako").

//go:wasmimport mako host_call
func hostCallRaw(methodPtr, methodLen, reqPtr, reqLen uint32) uint64

//go:wasmimport mako host_read
func hostReadRaw(dstPtr, dstLen uint32) int32

//go:wasmimport mako block_reload
func blockReloadRaw(idPtr, idLen, scopePtr, scopeLen uint32)

// pinned holds Go-allocated buffers handed to the host until mako_free runs.
var pinned = map[uint32][]byte{}

//go:wasmexport mako_alloc
func makoAlloc(size uint32) uint32 {
	if size == 0 {
		return 0
	}
	buf := make([]byte, size)
	ptr := uint32(uintptr(unsafe.Pointer(&buf[0])))
	pinned[ptr] = buf
	return ptr
}

//go:wasmexport mako_free
func makoFree(ptr, _ uint32) {
	delete(pinned, ptr)
}

func packReturn(data []byte) uint64 {
	if len(data) == 0 {
		return 0
	}
	ptr := makoAlloc(uint32(len(data)))
	copy(pinned[ptr], data)
	return (uint64(ptr) << 32) | uint64(len(data))
}

func packError(msg string) uint64 {
	buf := make([]byte, 1+len(msg))
	buf[0] = 0x01
	copy(buf[1:], msg)
	return packReturn(buf)
}

func slicePtr(b []byte) (uint32, uint32) {
	if len(b) == 0 {
		return 0, 0
	}
	return uint32(uintptr(unsafe.Pointer(&b[0]))), uint32(len(b))
}

func readPtr(ptr, length uint32) []byte {
	if ptr == 0 || length == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), length)
}

func callHost(method string, req any) ([]byte, error) {
	mb := []byte(method)
	rb, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	mp, ml := slicePtr(mb)
	rp, rl := slicePtr(rb)
	res := hostCallRaw(mp, ml, rp, rl)
	isErr := (res >> 32) != 0
	n := uint32(res & 0xffffffff)
	var out []byte
	if n > 0 {
		out = make([]byte, n)
		op, ol := slicePtr(out)
		got := hostReadRaw(op, ol)
		if got >= 0 && uint32(got) < n {
			out = out[:got]
		}
	}
	if isErr {
		return nil, fmt.Errorf("%s", string(out))
	}
	return out, nil
}

func requestReload(blockID, scope string) {
	bp, bl := slicePtr([]byte(blockID))
	sp, sl := slicePtr([]byte(scope))
	blockReloadRaw(bp, bl, sp, sl)
}

type runRecord struct {
	Run          int    `json:"run"`
	Limit        int    `json:"limit"`
	PrimeCount   int    `json:"primeCount"`
	LargestPrime int    `json:"largestPrime"`
	Digest       string `json:"digest"`
}

type pluginState struct {
	Limit   int         `json:"limit"`
	Runs    []runRecord `json:"runs"`
	LastMsg string      `json:"lastMsg"`
}

var st = pluginState{Limit: 10000}

type scopeCtx struct {
	Locale string `json:"locale"`
	Scope  string `json:"scope"`
	Tab    *struct {
		Key    string         `json:"key"`
		Params map[string]any `json:"params"`
	} `json:"tab"`
}

func isKo(locale string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(locale)), "ko")
}

func tr(locale, en, ko string) string {
	if isKo(locale) {
		return ko
	}
	return en
}

// runCompute performs a deterministic Sieve of Eratosthenes + SHA-256 digest
// directly inside the sandboxed WebAssembly module.
func runCompute(limit int) runRecord {
	if limit < 1000 {
		limit = 1000
	}
	if limit > 50000 {
		limit = 50000
	}
	sieve := make([]bool, limit+1)
	count := 0
	largest := 2
	h := sha256.New()
	for p := 2; p <= limit; p++ {
		if sieve[p] {
			continue
		}
		count++
		largest = p
		_, _ = fmt.Fprintf(h, "%d,", p)
		for m := p * p; m <= limit && m > 0; m += p {
			sieve[m] = true
		}
	}
	digest := hex.EncodeToString(h.Sum(nil))
	rec := runRecord{
		Run:          len(st.Runs) + 1,
		Limit:        limit,
		PrimeCount:   count,
		LargestPrime: largest,
		Digest:       digest,
	}
	st.Runs = append(st.Runs, rec)
	if len(st.Runs) > 8 {
		st.Runs = st.Runs[len(st.Runs)-8:]
	}
	st.LastMsg = fmt.Sprintf("Found %d primes <= %d (max %d)", count, limit, largest)
	return rec
}

func persistState() {
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	_, _ = callHost("storage.setItem", map[string]any{
		"key":   "wasm-inspector:state",
		"value": string(b),
	})
}

func loadPersistedState() {
	raw, err := callHost("storage.getItem", map[string]any{
		"key": "wasm-inspector:state",
	})
	if err != nil || len(raw) == 0 {
		return
	}
	var resp struct {
		Found bool   `json:"found"`
		Value string `json:"value"`
	}
	if json.Unmarshal(raw, &resp) == nil && resp.Found && resp.Value != "" {
		var saved pluginState
		if json.Unmarshal([]byte(resp.Value), &saved) == nil {
			if saved.Limit >= 1000 && saved.Limit <= 50000 {
				st.Limit = saved.Limit
			}
			st.Runs = saved.Runs
			st.LastMsg = saved.LastMsg
		}
	}
}

func openDetailTab(rec *runRecord, locale string) {
	key := "overview"
	title := tr(locale, "Wasm Report", "Wasm 리포트")
	params := map[string]any{"limit": st.Limit}
	if rec != nil {
		key = fmt.Sprintf("run-%d", rec.Run)
		title = fmt.Sprintf("Wasm #%d (≤%d)", rec.Run, rec.Limit)
		params = map[string]any{
			"run":          rec.Run,
			"limit":        rec.Limit,
			"primeCount":   rec.PrimeCount,
			"largestPrime": rec.LargestPrime,
			"digest":       rec.Digest,
		}
	}
	argBytes, _ := json.Marshal(map[string]any{
		"block":   "detail",
		"key":     key,
		"title":   title,
		"icon":    "radar",
		"params":  params,
		"preview": false,
	})
	_, _ = callHost("commands.runCommand", map[string]any{
		"id":  "mako.openTab",
		"arg": string(argBytes),
	})
}

func renderPanel(ctx scopeCtx) map[string]any {
	loc := ctx.Locale
	var latest *runRecord
	if len(st.Runs) > 0 {
		latest = &st.Runs[len(st.Runs)-1]
	}

	primeVal := "—"
	largestDelta := tr(loc, "Not run yet", "아직 실행 안 됨")
	digestShort := "—"
	if latest != nil {
		primeVal = strconv.Itoa(latest.PrimeCount)
		largestDelta = fmt.Sprintf("max %d", latest.LargestPrime)
		if len(latest.Digest) >= 12 {
			digestShort = latest.Digest[:12]
		}
	}

	share := float64(st.Limit) / 50000.0
	if share < 0 {
		share = 0
	}
	if share > 1 {
		share = 1
	}

	children := []any{
		map[string]any{
			"kind":    "row",
			"key":     "tb",
			"variant": "toolbar",
			"children": []any{
				map[string]any{"kind": "text", "text": tr(loc, "Wasm Lab", "Wasm 랩"), "weight": "semibold", "grow": 1},
				map[string]any{"kind": "button", "key": "tb-run", "icon": "rocket", "variant": "ghost", "size": "small", "onTap": []any{"runCompute"}},
				map[string]any{"kind": "button", "key": "tb-tab", "icon": "radar", "variant": "ghost", "size": "small", "onTap": []any{"openDetail"}},
				map[string]any{"kind": "button", "key": "tb-clear", "icon": "trash-2", "variant": "ghost", "size": "small", "onTap": []any{"clearHistory"}},
			},
		},
	}

	leftCol := []any{
		map[string]any{
			"kind":        "alert",
			"key":         "wasm-banner",
			"variant":     "soft",
			"tone":        "accent",
			"icon":        "code-xml",
			"text":        tr(loc, "Pure-Go WebAssembly Sandbox", "순수 Go WebAssembly 샌드박스"),
			"placeholder": tr(loc, "Compiled to plugin.wasm and executed by wasmtime-pure-go.", "plugin.wasm으로 컴파일되어 wasmtime-pure-go로 실행됩니다."),
		},
		map[string]any{
			"kind":    "grid",
			"key":     "wasm-stats",
			"columns": 2,
			"gap":     8,
			"children": []any{
				map[string]any{
					"kind":        "stat",
					"key":         "st-primes",
					"text":        tr(loc, "Primes", "소수 개수"),
					"value":       primeVal,
					"placeholder": largestDelta,
					"icon":        "sparkles",
					"tone":        "success",
					"size":        "small",
					"onTap":       []any{"runCompute"},
				},
				map[string]any{
					"kind":        "stat",
					"key":         "st-runs",
					"text":        tr(loc, "Runs", "실행 횟수"),
					"value":       strconv.Itoa(len(st.Runs)),
					"placeholder": digestShort,
					"icon":        "radar",
					"tone":        "accent",
					"size":        "small",
					"onTap":       []any{"openDetail"},
				},
			},
		},
		map[string]any{
			"kind": "card",
			"key":  "wasm-controls",
			"text": tr(loc, "Sieve + SHA-256 Workload", "소수 체 + SHA-256 연산"),
			"icon": "sliders-horizontal",
			"children": []any{
				map[string]any{
					"kind": "column",
					"gap":  8,
					"children": []any{
						map[string]any{
							"kind":     "slider",
							"key":      "limit-slider",
							"value":    strconv.Itoa(st.Limit),
							"min":      1000,
							"max":      50000,
							"step":     1000,
							"text":     fmt.Sprintf("%s: %d", tr(loc, "Upper bound", "상한값"), st.Limit),
							"onChange": []any{"setLimit"},
						},
						map[string]any{
							"kind": "row",
							"gap":  8,
							"children": []any{
								map[string]any{"kind": "progress", "key": "ring", "variant": "ring", "progress": share, "tone": "accent"},
								map[string]any{
									"kind":    "button",
									"key":     "btn-run",
									"text":    tr(loc, "Run in Wasm", "Wasm에서 실행"),
									"icon":    "rocket",
									"variant": "primary",
									"size":    "small",
									"grow":    1,
									"onTap":   []any{"runCompute"},
								},
							},
						},
						map[string]any{
							"kind":    "button",
							"key":     "btn-open-tab",
							"text":    tr(loc, "Open Report Tab", "리포트 탭 열기"),
							"icon":    "radar",
							"variant": "outline",
							"size":    "small",
							"onTap":   []any{"openDetail"},
						},
					},
				},
			},
		},
	}

	var rightCol []any
	if latest != nil {
		rightCol = append(rightCol, map[string]any{
			"kind": "card",
			"key":  "wasm-digest-card",
			"text": tr(loc, "Latest SHA-256", "최근 SHA-256"),
			"icon": "file-code",
			"children": []any{
				map[string]any{
					"kind":     "code",
					"key":      "digest-code",
					"language": "json",
					"text": fmt.Sprintf("{\n  \"run\": %d,\n  \"limit\": %d,\n  \"primes\": %d,\n  \"largest\": %d,\n  \"sha256\": %q\n}",
						latest.Run, latest.Limit, latest.PrimeCount, latest.LargestPrime, latest.Digest),
				},
			},
		})
		histRows := make([]any, 0, len(st.Runs))
		for i := len(st.Runs) - 1; i >= 0; i-- {
			r := st.Runs[i]
			short := r.Digest
			if len(short) > 14 {
				short = short[:14] + "…"
			}
			histRows = append(histRows, map[string]any{
				"kind": "row",
				"key":  fmt.Sprintf("pr-%d", r.Run),
				"children": []any{
					map[string]any{"kind": "text", "text": fmt.Sprintf("#%d", r.Run), "variant": "mono", "size": "small"},
					map[string]any{"kind": "text", "text": strconv.Itoa(r.Limit), "variant": "mono", "size": "small"},
					map[string]any{"kind": "text", "text": strconv.Itoa(r.PrimeCount), "variant": "mono", "size": "small"},
					map[string]any{"kind": "text", "text": short, "variant": "mono", "size": "small", "tone": "muted"},
				},
			})
		}
		rightCol = append(rightCol, map[string]any{
			"kind": "card",
			"key":  "wasm-history-card",
			"text": tr(loc, "Benchmark History", "벤치마크 실행 기록"),
			"icon": "radar",
			"children": []any{
				map[string]any{
					"kind":     "table",
					"key":      "panel-hist-table",
					"variant":  "striped",
					"labels":   []string{"Run", "Limit", "Primes", "SHA-256"},
					"children": histRows,
				},
			},
		})
	} else {
		rightCol = append(rightCol, map[string]any{
			"kind":        "empty",
			"key":         "wasm-empty",
			"icon":        "code-xml",
			"tone":        "muted",
			"text":        tr(loc, "No Wasm runs yet", "아직 실행 기록 없음"),
			"placeholder": tr(loc, "Click Run in Wasm to compute primes & SHA-256 inside WebAssembly.", "'Wasm에서 실행'을 눌러 WebAssembly 샌드박스 안에서 연산해 보세요."),
		})
	}

	children = append(children, map[string]any{
		"kind":      "split",
		"key":       "panel-split",
		"direction": "horizontal",
		"ratio":     0.44,
		"grow":      1,
		"children": []any{
			map[string]any{
				"kind": "scroll",
				"key":  "panel-left-scroll",
				"children": []any{
					map[string]any{"kind": "column", "gap": 10, "children": leftCol},
				},
			},
			map[string]any{
				"kind": "scroll",
				"key":  "panel-right-scroll",
				"children": []any{
					map[string]any{"kind": "column", "gap": 10, "children": rightCol},
				},
			},
		},
	})

	return map[string]any{
		"kind":     "column",
		"fill":     true,
		"gap":      8,
		"children": children,
	}
}

func renderDetailTab(ctx scopeCtx) map[string]any {
	loc := ctx.Locale
	tabKey := "overview"
	var tabParams map[string]any
	if ctx.Tab != nil {
		if ctx.Tab.Key != "" {
			tabKey = ctx.Tab.Key
		}
		tabParams = ctx.Tab.Params
	}

	tableRows := make([]any, 0, len(st.Runs))
	for i := len(st.Runs) - 1; i >= 0; i-- {
		r := st.Runs[i]
		short := r.Digest
		if len(short) > 16 {
			short = short[:16] + "…"
		}
		tableRows = append(tableRows, map[string]any{
			"kind": "row",
			"key":  fmt.Sprintf("r-%d", r.Run),
			"children": []any{
				map[string]any{"kind": "text", "text": fmt.Sprintf("#%d", r.Run), "variant": "mono", "size": "small"},
				map[string]any{"kind": "text", "text": strconv.Itoa(r.Limit), "variant": "mono", "size": "small"},
				map[string]any{"kind": "text", "text": strconv.Itoa(r.PrimeCount), "variant": "mono", "size": "small"},
				map[string]any{"kind": "text", "text": strconv.Itoa(r.LargestPrime), "variant": "mono", "size": "small"},
				map[string]any{"kind": "text", "text": short, "variant": "mono", "size": "small", "tone": "muted"},
			},
		})
	}

	metaJSON, _ := json.MarshalIndent(map[string]any{
		"runtime": "wasmtime-pure-go (Core Wasm ABI)",
		"tabKey":  tabKey,
		"params":  tabParams,
		"runs":    len(st.Runs),
	}, "", "  ")

	var historyWidget any
	if len(tableRows) == 0 {
		historyWidget = map[string]any{
			"kind":        "empty",
			"key":         "det-empty",
			"icon":        "radar",
			"tone":        "muted",
			"text":        tr(loc, "No benchmark runs recorded", "기록된 벤치마크 실행 없음"),
			"placeholder": tr(loc, "Run a WebAssembly benchmark to populate this table.", "WebAssembly 벤치마크를 실행하면 결과가 표에 기록됩니다."),
			"children": []any{
				map[string]any{"kind": "button", "key": "det-run-empty", "text": tr(loc, "Run now", "지금 실행"), "variant": "primary", "size": "small", "onTap": []any{"runCompute"}},
			},
		}
	} else {
		historyWidget = map[string]any{
			"kind":     "table",
			"key":      "det-table",
			"variant":  "striped",
			"labels":   []string{"Run", "Limit", "Primes", "Largest", "SHA-256"},
			"children": tableRows,
		}
	}

	return map[string]any{
		"kind": "column",
		"fill": true,
		"gap":  10,
		"children": []any{
			map[string]any{
				"kind":    "row",
				"key":     "det-tb",
				"variant": "toolbar",
				"children": []any{
					map[string]any{"kind": "chip", "text": "wasm:" + tabKey, "icon": "code-xml", "variant": "soft", "tone": "accent"},
					map[string]any{"kind": "spacer"},
					map[string]any{"kind": "button", "key": "det-run", "text": tr(loc, "Run Again", "다시 실행"), "icon": "rocket", "variant": "primary", "size": "small", "onTap": []any{"runCompute"}},
					map[string]any{"kind": "button", "key": "det-close", "text": tr(loc, "Close", "닫기"), "icon": "x", "variant": "ghost", "size": "small", "onTap": []any{"closeDetail"}},
				},
			},
			map[string]any{
				"kind": "scroll",
				"key":  "det-scroll",
				"grow": 1,
				"children": []any{
					map[string]any{
						"kind": "column",
						"gap":  12,
						"children": []any{
							map[string]any{
								"kind": "card",
								"key":  "det-meta-card",
								"text": tr(loc, "WebAssembly Instance Context", "WebAssembly 인스턴스 컨텍스트"),
								"icon": "code-xml",
								"children": []any{
									map[string]any{"kind": "code", "key": "det-meta", "language": "json", "text": string(metaJSON)},
								},
							},
							map[string]any{
								"kind":     "card",
								"key":      "det-hist-card",
								"text":     tr(loc, "Run History", "실행 기록"),
								"icon":     "radar",
								"children": []any{historyWidget},
							},
						},
					},
				},
			},
		},
	}
}

//go:wasmexport mako_invoke
func makoInvoke(methodPtr, methodLen, argsPtr, argsLen uint32) uint64 {
	method := string(readPtr(methodPtr, methodLen))
	argsRaw := readPtr(argsPtr, argsLen)

	var args []json.RawMessage
	if len(argsRaw) > 0 {
		_ = json.Unmarshal(argsRaw, &args)
	}
	strArg := func(i int) string {
		if i >= len(args) {
			return ""
		}
		var s string
		if json.Unmarshal(args[i], &s) == nil {
			return s
		}
		return string(args[i])
	}

	switch method {
	case "activate":
		return 0

	case "blocks":
		return packReturn([]byte(`["panel","detail"]`))

	case "refresh":
		return packReturn([]byte("0"))

	case "load":
		loadPersistedState()
		return 0

	case "render":
		blockID := strArg(0)
		var ctx scopeCtx
		_ = json.Unmarshal([]byte(strArg(2)), &ctx)
		var tree map[string]any
		if blockID == "detail" {
			tree = renderDetailTab(ctx)
		} else {
			tree = renderPanel(ctx)
		}
		b, err := json.Marshal(tree)
		if err != nil {
			return packError("marshal tree: " + err.Error())
		}
		return packReturn(b)

	case "event":
		var ctx scopeCtx
		_ = json.Unmarshal([]byte(strArg(2)), &ctx)
		evName := strArg(3)
		var evArgs []json.RawMessage
		_ = json.Unmarshal([]byte(strArg(4)), &evArgs)

		switch evName {
		case "setLimit":
			if len(evArgs) > 0 {
				var s string
				var f float64
				if json.Unmarshal(evArgs[0], &s) == nil {
					if v, err := strconv.ParseFloat(s, 64); err == nil {
						st.Limit = int(v)
					}
				} else if json.Unmarshal(evArgs[0], &f) == nil {
					st.Limit = int(f)
				}
				if st.Limit < 1000 {
					st.Limit = 1000
				}
				if st.Limit > 50000 {
					st.Limit = 50000
				}
				persistState()
			}
		case "runCompute":
			rec := runCompute(st.Limit)
			persistState()
			requestReload("panel", "")
			requestReload("detail", "")
			_, _ = callHost("commands.runCommand", map[string]any{
				"id":  "mako.toast",
				"arg": fmt.Sprintf("Wasm #%d: %d primes (≤%d)", rec.Run, rec.PrimeCount, rec.Limit),
			})
		case "openDetail":
			var latest *runRecord
			if len(st.Runs) > 0 {
				latest = &st.Runs[len(st.Runs)-1]
			}
			openDetailTab(latest, ctx.Locale)
		case "closeDetail":
			tabKey := "overview"
			if ctx.Tab != nil && ctx.Tab.Key != "" {
				tabKey = ctx.Tab.Key
			}
			argBytes, _ := json.Marshal(map[string]any{
				"block": "detail",
				"key":   tabKey,
			})
			_, _ = callHost("commands.runCommand", map[string]any{
				"id":  "mako.closeTab",
				"arg": string(argBytes),
			})
		case "clearHistory":
			st.Runs = nil
			st.LastMsg = ""
			_, _ = callHost("storage.deleteItem", map[string]any{"key": "wasm-inspector:state"})
			requestReload("panel", "")
			requestReload("detail", "")
		}
		return 0

	case "command":
		cmdID := strArg(0)
		var ctx scopeCtx
		_ = json.Unmarshal([]byte(strArg(1)), &ctx)
		if cmdID == "run-bench" {
			rec := runCompute(st.Limit)
			persistState()
			openDetailTab(&rec, ctx.Locale)
			_, _ = callHost("commands.runCommand", map[string]any{
				"id":  "mako.toast",
				"arg": fmt.Sprintf("Wasm bench #%d: %d primes (≤%d)", rec.Run, rec.PrimeCount, rec.Limit),
			})
		}
		return 0

	case "save":
		b, _ := json.Marshal(st)
		return packReturn(b)

	case "restore":
		raw := strArg(2)
		if raw != "" && raw != "null" {
			var saved pluginState
			if json.Unmarshal([]byte(raw), &saved) == nil {
				st = saved
			}
		}
		return 0

	case "takeReloads":
		return packReturn([]byte("[]"))

	default:
		return 0
	}
}

func main() {}
