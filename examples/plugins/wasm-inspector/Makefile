.PHONY: build clean

build:
	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -ldflags="-s -w" -o plugin.wasm ./src

clean:
	rm -f plugin.wasm
