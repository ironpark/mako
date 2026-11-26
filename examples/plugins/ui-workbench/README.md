# UI Workbench (`ui-workbench`)

Revision 49 플러그인 시스템의 새로운 UI 영역과 컴포넌트를 종합적으로 시연하는 JavaScript 데모 플러그인입니다.

## 포함된 기능
- **좌측 사이드바 메뉴 → 중앙 콘텐츠 뷰** (`slots = ["sidebar", "left", "right"]`, `icon = "flask-conical"`, `chrome = "toolbar"`):
  - 좌측 사이드바 상단 메뉴 항목으로 추가되며, 활성화 시 중앙 콘텐츠 영역에 렌더링됩니다
  - 헤더로 승격되는 `ui.toolbar`, `ui.input(variant="search")`, `ui.stat`, `ui.progress(variant="ring")`, `ui.empty`
  - 항목 클릭 시 프리뷰 탭 열기 / 핀 아이콘 클릭 시 고정 탭 열기
- **커스텀 워크스페이스 탭** (`slots = ["tab"]`, `icon = "file-diff"`):
  - `ctx.tab.key` / `ctx.tab.params` 기반 멀티 인스턴스 탭 (`ctx.ui.openTab`, `ctx.ui.closeTab`)
  - **Diff 리뷰**: `ui.diff`(`unified`/`split` 전환), 라인 클릭 코멘트 작성(`ui.textarea(variant="mono")`, `ctx.storage` 저장), `ui.code`(구문 강조 + 복사 버튼), `ui.alert`(닫기 가능), `ui.kbd`
  - **컴포넌트 스튜디오**: `ui.stat`, `ui.slider` ↔ `ui.progress`(`ring`/`spinner`) 실시간 연동, `ui.tabs`(`underline`/`pills`), `ui.radio`, `ui.input(variant="password")`, 정렬 가능한 `ui.table`(`striped`/`compact`), `ui.card` 헤더/배지, `ui.skeleton`, `ui.empty`, `ui.spacer`
- **한국어/영어 다국어 지원** (`mako.i18n`, `ctx.t`)

## 로컬 설치 방법
```bash
cp -R examples/plugins/ui-workbench ~/.config/mako/plugins/ui-workbench
```
(macOS 기본 설정 폴더는 `~/Library/Application Support/mako/plugins/ui-workbench` 또는 `MAKO_HOME/plugins/ui-workbench`)
설치 후 **설정 ▸ 플러그인**에서 `storage` 권한을 허용하면 좌측 사이드바 상단 아이콘 바에 **Workbench** 페이지가 바로 나타납니다.
