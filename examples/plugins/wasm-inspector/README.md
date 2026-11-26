# Wasm Inspector (`wasm-inspector`)

순수 Go 코드를 `GOOS=wasip1 GOARCH=wasm`으로 컴파일하여 Mako의 **Revision 49 WebAssembly 런타임(`wasmtime-pure-go`, `CGO_ENABLED=0`)**에서 실행하는 데모 플러그인입니다.

## 포함된 기능
- **Core WebAssembly ABI (`entry = "plugin.wasm"`)**:
  - Host imports (`//go:wasmimport mako`): `host_call`, `host_read`, `block_reload`
  - Guest exports (`//go:wasmexport`): `mako_alloc`, `mako_free`, `mako_invoke` + WASI Reactor `_initialize`
- **샌드박스 내부 네이티브 연산**:
  - `ui.slider`로 상한(1,000~50,000)을 조절하고 WebAssembly 안에서 에라토스테네스의 체 + SHA-256 다이제스트를 직접 계산
  - 결과를 `storage.setItem` / `storage.getItem`으로 영속화하고 `mako.toast` 알림 전송
- **사이드바 페이지 + 커스텀 워크스페이스 탭**:
  - 좌측 사이드바(`panel`)에서 `commands.runCommand("mako.openTab")`을 호출해 멀티 인스턴스 리포트 탭(`detail`)을 열고 `ui.code`, `ui.table`, 한국어/영어 로케일을 표시

## 다시 빌드하기
```bash
cd examples/plugins/wasm-inspector
make build
```

## 로컬 설치 방법
```bash
cp -R examples/plugins/wasm-inspector ~/.config/mako/plugins/wasm-inspector
```
(또는 사용 중인 Mako 설정 디렉토리의 `plugins/wasm-inspector`)
