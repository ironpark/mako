# Mako 경쟁 제품 분석 및 제품 개선 로드맵 (Claude Code · Herdr · Orca 대비)

작성 기준일: 2026년 11월 28일.
상태: **P0 (#8, #9, #10)** 및 **P1 (#11, #12, #14)** 구현 PR 제출 완료 · **P2** 기획 대기.

본 문서는 멀티 에이전트 코딩 워크스페이스/오케스트레이터 시장의 주요 제품인 **Claude Code (CLI/Desktop)**, **Herdr**, **Orca (Stably AI)** 와 현재 Mako 코드베이스를 기획·아키텍처 관점에서 비교 분석하고, Mako의 고유 강점을 살려 경쟁 우위를 확보하기 위한 우선순위별(P0 / P1 / P2) 개선 로드맵을 정리한다.

---

## 1. Mako의 고유 강점과 경쟁 포지셔닝

개선 과제를 도출하기 앞서, Mako가 이미 경쟁 제품 대비 뚜렷한 우위를 점하고 있는 핵심 자산을 먼저 정의한다. 향후 모든 기획은 이 기반 위에 얹는 방향을 취한다.

* **Private P2P 메시 + Zero-install Direct SSH 원격 실행**: 여러 머신(노트북, 빌드 박스, 홈서버, SSH 호스트)을 단일 창과 단일 프로젝트 모델(`repo:` 원격 일치)로 통합하고, 크로스 노드 `spawn_agent`/메시징, 원격 파일 브라우저, `localhost` 포트 포워딩, **Android 기기 공유(`adb borrow`)** 까지 제공.
* **멀티 하네스(Multi-Harness) 통합**: Claude Code, Codex, Antigravity(`agy`), OpenCode, pi/omp/omo, Shell을 단일 UI/프로토콜로 구동.
* **샌드박스 플러그인 플랫폼(Rev 49) & 프로그래밍 가능한 프롬프트 템플릿(`//name`)**: QuickJS/Wasmtime 샌드박스, Scoped Storage/SQLite/CRDT, 에이전트 자체 UI(`show_ui`), Svelte/JS 기반 멀티 에이전트 템플릿(`mako.ask`/`mako.spawn`/`mako.decide`).
* **영구 데몬 아키텍처(`makod`) + Android 컴패니언 앱**: 클라이언트 종료와 무관하게 세션 생존, 모바일 알림 승인·채팅·터미널 제어 지원.

### 경쟁 제품별 핵심 포지셔닝 비교

| 항목 | Mako (기준 시점) | Claude Code (CLI/Desktop) | Herdr (`herdr.dev`) | Orca (`stablyai/orca`) |
| :--- | :--- | :--- | :--- | :--- |
| **핵심 컨셉** | 분산 P2P 메시 + 멀티 하네스 워크스페이스 | 단일 벤더 딥 인티그레이션 코딩 에이전트 | AI 에이전트 전용 tmux / 터미널 멀티플렉서 | Worktree-First Agent Development Environment |
| **멀티 머신 / 원격** | P2P 메시 + Direct SSH + 원클릭 `makod` 설치 | Remote Control (모바일 연동) / Cloud Sandbox | SSH 기반 멀티 머신 집계 | 로컬 중심 + iOS/Android 알림 |
| **워크트리 격리** | 수동 토글 (`git worktree add`만 수행) | `--worktree` / `isolation: "worktree"` / `WorktreeCreate` 훅 | 일반 터미널 팬 | 태스크별 자동 워크트리 + `orca.yaml` 공유 디렉토리 |
| **코드 리뷰 · 프리뷰** | 단일 선택 라인 전송, Hunk Discard | 터미널/데스크톱 Diff | 터미널 중심 | 멀티 파일 인라인 리뷰 + A/B 비교 + 내장 Chromium Design Mode |
| **되돌리기 (Rewind)** | 대화만 포크 (Claude/Codex 한정) | `Esc+Esc` 대화 + 파일 상태 체크포인트 롤백 | — | Git 워크트리 단위 |
| **어텐션 / 트리아지** | 데스크톱 Waiting Pill 순회 (Android는 Needs You 큐) | `claude agents` TUI 뷰 | 중앙 Attention Queue + PTY 기반 상태 추론 | 통합 사이드바 + 모바일 푸시 |
| **무인 자동화** | 수동 실행 위주 | `/loop`, Desktop Scheduled Tasks, Cloud Routines | CLI / Socket API 스크립팅 | 코디네이터 + Linear/GitHub 연동 |
| **가드레일 (정책/훅/샌드박스)** | 개별 에이전트 승인 중계 | `PreToolUse`/`PostToolUse` 훅, 세밀한 규칙, OS/클라우드 샌드박스 | 프로세스 수준 | 하네스 기본 권한 |
| **플랫폼 & 온보딩** | macOS 주력, Linux, Windows(실험), Android(iOS 부재), 소스 빌드 | macOS/Linux/Windows, iOS/Android, 원라인 설치 | macOS/Linux TUI+Desktop | macOS/Windows/Linux, iOS/Android, 인스톨러 |

---

## 2. 영역별 갭 분석 (Gap Analysis)

### 2.1 워크트리 격리 및 병렬 실행 *(vs. Orca, Claude Code)*
* **워크트리 부트스트랩 / 공유 디렉토리 부재**: `internal/gitstat/worktree.go`의 `AddWorktree`가 순수 `git worktree add -b`만 수행하므로, 모노레포·프론트엔드 프로젝트에서 워크트리 생성마다 `node_modules`·`.cache`·`.env` 및 초기화 명령을 수동으로 처리해야 하는 마찰이 큼.
* **멀티 에이전트 스폰 / 팀 실행 시 자동 격리 부재**: MCP `spawn_agent`(`internal/daemon/agent_mcp_tools.go`)에 `worktree` 격리 인자가 없고, `startTeam`(`internal/gui/teams.go`)도 모든 팀원이 부모와 동일한 워크트리를 공유해 병렬 코드 수정 시 파일 충돌이 발생함.
* **동일 프롬프트 A/B 비교 실행 부재**: Launch 탭(`internal/gui/launchtab.go`)이 단일 에이전트(또는 단일 팀)만 시작할 수 있어, Claude vs Codex 같은 멀티 에이전트 Best-of-N 비교 및 승자 머지 플로우가 없음.

### 2.2 코드 리뷰 · 시각적 프리뷰 · 안전한 되돌리기 *(vs. Orca, Claude Code)*
* **파일 상태 포함 Rewind 부재**: `internal/gui/chat.go`의 `askRewindSession`은 대화만 포크(Claude/Codex 한정)할 뿐 작업 트리의 파일 변경분은 되돌리지 못해, 에이전트가 잘못된 방향으로 코드를 수정했을 때 안전한 원클릭 복구가 어려움.
* **멀티 파일 배치 코드 리뷰(Draft Review) & Hunk Stage/Unstage 부재**: `internal/gui/difftab.go`에서 라인 선택 후 즉시 전송 및 Hunk Discard는 가능하나, 여러 파일에 걸쳐 리뷰 코멘트를 모아 구조화된 배치로 전송하거나 해결 여부를 추적하고 Hunk 단위로 Stage/Unstage하는 워크플로우가 부족함.
* **내장 웹 프리뷰 & Visual Design Mode 부재**: `internal/gui/browser.go`는 원격 노드 파일 브라우저이며 `localhost` 프리뷰는 외부 브라우저로 열림. Orca처럼 DOM 요소를 클릭해 HTML/CSS/Selector/크롭 스크린샷을 에이전트로 즉시 넘기는 시각적 피드백 루프가 없음.

### 2.3 대규모 세션 어텐션 · TUI · 스크립터블 CLI *(vs. Herdr, Claude Code)*
* **데스크톱 중앙 집중식 Attention / Approval Triage 뷰 부재**: Android 앱(`app/README.md`)에는 "Needs you" 통합 큐가 있으나, 데스크톱(`internal/gui/attention.go`)은 상단 `waitingPill`(`⌘⇧A` 순회)만 존재. 세션이 10~20개 이상 실행될 때 승인·질문·인증오류·완료를 한 화면에서 트리아지하고 일괄 승인할 Command Center가 부족함.
* **TUI 멀티플렉서 및 자동화 CLI 부족**: Herdr의 고속 TUI나 `claude agents` 뷰에 해당하는 터미널 UI가 없으며, `cmd/mako/main.go`에 외부 스크립트·CI 연동용 `send` / `wait` / `tail` / `approve` / `--json` 프리미티브가 부족함.
* **비-Hook 에이전트 상태 감지 한계**: `docs/usage.md` 기준 정확한 `waiting` 보고와 앱 내 승인 카드는 주로 Claude Code에 집중되어 있으며, 기타 하네스용 PTY/화면 기반 상태 추론 폴백 고도화 여지가 큼.

### 2.4 무인 자동화 · 공통 가드레일(정책/훅/샌드박스) *(vs. Claude Code)*
* **스케줄러 & 이벤트 트리거(Routines) 부재**: 상시 실행 데몬(`makod`) + Forge API + 프롬프트 템플릿이라는 최적의 인프라를 보유하고도, 크론 스케줄이나 PR 오픈·CI 실패 이벤트로 에이전트를 자동 실행하고 이력을 관리하는 엔진이 없음.
* **하네스 횡단 권한 정책 & 라이프사이클 훅 부재**: `internal/gui/approval.go`가 개별 에이전트 승인을 중계할 뿐, 모든 하네스에 공통 적용되는 자동 허용/차단 규칙(Deny 우선, 감사 로그)이나 턴 완료 시 자동 `gofmt`/테스트 등을 실행하는 결정론적 훅이 없음.
* **OS 샌드박스 실행 모드 부재**: 자율/YOLO 실행 시 프로젝트/워크트리 밖 파일 쓰기를 차단하는 macOS `sandbox-exec` / Linux `bwrap` 기반 워크스페이스 샌드박스 프로파일이 없음.

### 2.5 실무 툴 연동 · 쿼터 활용 · 플랫폼/온보딩 *(vs. Orca, Claude Code)*
* **이슈 트래커 확장성**: GitHub/Forgejo/Gitea는 탄탄하지만 실무 비중이 높은 Linear/Jira 이슈 → 워크트리+에이전트 스폰 연동이 기본 제공되지 않음.
* **계정 쿼터의 읽기 전용 한계 & 비용 분석 부재**: `internal/gui/accountusage.go`로 멀티 하네스 쿼터 조회는 지원하지만, 한도 임박 시 대체 에이전트/프로파일 원클릭 전환이나 프로젝트·세션 단위 토큰/비용 집계 뷰가 없음.
* **iOS 부재 및 초기 설치 진입장벽**: macOS 데스크톱이 주력임에도 모바일 앱은 Android 전용이며(iOS/경량 PWA 부재), Windows는 Experimental, 배포도 소스 빌드(`make build` + `ggui`) 중심.

---

## 3. 우선순위별 개선 로드맵 (P0 / P1 / P2)

### P0 — 즉시 체감 효과가 큰 핵심 워크플로우 보강 *(PR 제출 완료)*

| # | 과제 | 핵심 구현 내용 | PR |
|---|---|---|---|
| **P0-1** | **Worktree-First 병렬 실행 & 부트스트랩** | `.mako/worktree.toml`(`base_ref`, `shared_paths`, `copy_paths`, `setup_cmd`, `teardown_cmd`) 및 `orca.yaml` 호환 폴백, 실패 시 자동 롤백, MCP `spawn_agent`의 `worktree`/`branch`/`base` 격리, **Settings ▸ Teams** 팀원별 워크트리 격리, CLI `mako -worktree / -branch run` | [#8](https://github.com/ironpark/mako/pull/8) |
| **P0-2** | **데스크톱 "Needs You" Inbox / 트리아지 뷰** | 좌측 사이드바 상단·Command Palette·단축키(`Cmd+Shift+I`) 통합 트리아지 뷰(`builtin:inbox`), 우선순위 정렬(Approvals → Auth Errors → Waiting → Unread), 필터, 카드 내 즉시 승인/거부/빠른 답장/Dismiss, 안전한 도구 승인 일괄 허용(`Allow all approvals`) | [#9](https://github.com/ironpark/mako/pull/9) |
| **P0-3** | **멀티 파일 Draft Review · Hunk Stage/Unstage · 파일 포함 Rewind** | Diff 라인 선택의 **Add to review** 누적 큐 및 구조화 배치 전송·해결 추적, `git apply --cached` 기반 Hunk Stage/Unstage, 임시 인덱스(`GIT_INDEX_FILE`) 기반 Shadow Git Checkpoint 자동 캡처(`refs/mako/checkpoints/...`) + **대화+코드 / 대화만 / 코드만** 선택 Rewind 모달 | [#10](https://github.com/ironpark/mako/pull/10) |

### P1 — Mako만의 차별화 무기화 *(PR 제출 완료)*

| # | 과제 | 핵심 구현 내용 | PR |
|---|---|---|---|
| **P1-4** | **Mako Routines (Self-Hosted 스케줄 & 이벤트 트리거)** | 무의존 5-field 크론 + `@hourly`/`@daily`/`@weekly`/`@every` 스케줄러(`internal/routines`), `makod` 백그라운드 크론 및 Forge 이벤트(`forge_pr_opened`, `forge_ci_failed`) 실행·이력 기록(`routines.json`/`routines-history.json`), CLI `mako routines`, **Settings ▸ Routines** UI | [#11](https://github.com/ironpark/mako/pull/11) |
| **P1-5** | **Cross-Agent 권한 정책 · 공통 훅 · OS 워크스페이스 샌드박스** | 글로벌(`policies.json`) + 프로젝트 Deny 전용(`.mako/policy.toml`) 권한 엔진(`internal/policy`)과 `makod` 승인 선평가·감사 로그, `on_session_start`/`on_turn_done`/`on_session_end` 결정론적 훅, `agentspec`의 `sandbox: "workspace"`(macOS `sandbox-exec` / Linux `bwrap`), **Settings ▸ Policies & hooks** UI | [#12](https://github.com/ironpark/mako/pull/12) |
| **P1-6** | **Multi-Agent A/B 비교 실행(Race) & 쿼터 전환/사용량 분석** | Launch 탭 **Compare** 다중 에이전트 동시 워크트리 실행 + **Compare agent races** 비교·승자 머지/패자 정리 모달, Account Usage의 85% 한도 경고·최저 사용률 대안 원클릭 전환(다이얼로그 & Launch 탭)·프로젝트별 토큰/비용 집계 탭 | [#14](https://github.com/ironpark/mako/pull/14) |

### P2 — 생태계 확장 및 사용자층 확대 *(기획 대기)*

1. **모바일/원격 접근성 확장 (iOS 또는 경량 Web/PWA 클라이언트)**
   * `makod`가 Tailscale/Mesh 내부에서 서빙하는 모바일 최적화 Web UI/PWA(+ Web Push)를 우선 제공해 iPhone/iPad 사용자의 승인·채팅·모니터링 공백 해소.
2. **Herdr급 TUI 대시보드 & 스크립터블 CLI 확장**
   * SSH/터미널 중심 사용자를 위한 `mako tui`(사이드바, 상태 배지, 어텐션 큐, 팬 attach)와 외부 자동화용 `mako send / wait / tail / approve --json` 추가.
   * 비-Hook 하네스 대상 PTY 화면 패턴 기반 `working / blocked / done / idle` 폴백 감지기 고도화.
3. **내장 웹 프리뷰(Design Mode) · 이슈 트래커 플러그인 · 원라인 배포**
   * 경량 WebView 프리뷰 + DOM 요소 인스펙터(선택 요소 HTML/CSS/스크린샷 → 현재 세션 composer 전송).
   * Mako 플러그인 플랫폼(Rev 49)을 활용한 **Linear / Jira 공식 퍼스트파티 플러그인** 제공 및 `brew`/공식 사전 빌드 릴리스(macOS DMG, Linux, Windows 검증)로 1분 온보딩 달성.

---

## 4. 한 줄 전략 요약

Mako는 경쟁 제품들이 쉽게 복제할 수 없는 **"분산 머신 P2P 메시 + 멀티 하네스 + 샌드박스 플러그인/템플릿 런타임"** 인프라를 이미 보유하고 있다.
**P0(워크트리 격리·셋업 + 데스크톱 Needs-You 트리아지 + 파일 Rewind/배치 리뷰)** 로 데일리 코딩 루프의 마찰을 없애고, **P1(Self-Hosted Routines + 하네스 횡단 정책/훅/샌드박스 + A/B 비교/쿼터 액션화)** 로 Claude Code·Herdr·Orca의 핵심 장점을 메시 아키텍처 위에서 통합함으로써, 멀티 머신·멀티 에이전트 영역의 독보적인 포지션을 구축할 수 있다.
