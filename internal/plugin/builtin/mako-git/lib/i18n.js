// Localization catalog and formatting helpers for mako-git.
// English keys match Mako's native Git panel strings; Korean entries match
// internal/gui/i18n/ko.go verbatim.

const en = {
  "Git": "Git",
  "Changes": "Changes",
  "Stashes": "Stashes",
  "Branches": "Branches",
  "History": "History",
  "Remote": "Remote",
  "Refresh": "Refresh",
  "Loading…": "Loading…",
  "Dismiss": "Dismiss",
  "Cancel": "Cancel",
  "Optional": "Optional",

  // Empty & error states
  "Open a session to see its changes.": "Open a session to see its changes.",
  "Not a git repository.": "Not a git repository.",
  "No changes.": "No changes.",
  "No stashes.": "No stashes.",
  "No commits yet.": "No commits yet.",
  "No files changed.": "No files changed.",
  "binary": "binary",

  // Toolbar & sync
  "Branch": "Branch",
  "Git actions": "Git actions",
  "vs HEAD": "vs HEAD",
  "since {base}": "since {base}",
  "Compare with HEAD, or with the branch the worktree started from": "Compare with HEAD, or with the branch the worktree started from",
  "Fetch": "Fetch",
  "Fetching…": "Fetching…",
  "Pull": "Pull",
  "Pull (rebase)": "Pull (rebase)",
  "Pull from {upstream}": "Pull from {upstream}",
  "Pulling…": "Pulling…",
  "Push": "Push",
  "Push to {upstream}": "Push to {upstream}",
  "Pushing…": "Pushing…",
  "Create pull request": "Create pull request",
  "Create pull request…": "Create pull request…",
  "New branch": "New branch",
  "New branch…": "New branch…",
  "Stash changes": "Stash changes",
  "Stash changes…": "Stash changes…",
  "Discard all changes…": "Discard all changes…",
  "Merge into {branch}": "Merge into {branch}",
  "Merge into the base branch": "Merge into the base branch",
  "Open a pull request": "Open a pull request",
  "Remove worktree": "Remove worktree",

  // Changes & commit box
  "Commit {name}": "Commit {name}",
  "Commit message": "Commit message",
  "Enter a commit message.": "Enter a commit message.",
  "Amend": "Amend",
  "Amend last commit": "Amend last commit",
  "Commit 1 file": "Commit 1 file",
  "Commit {count} files": "Commit {count} files",
  "Committing…": "Committing…",
  "Open diff": "Open diff",
  "Open file": "Open file",
  "Discard changes…": "Discard changes…",
  "Add to .gitignore": "Add to .gitignore",
  "Ignoring…": "Ignoring…",
  "Copy path": "Copy path",
  "{count} files": "{count} files",

  // Branches
  "Branch name": "Branch name",
  "Create branch": "Create branch",
  "Making branch…": "Making branch…",
  "Switching branch…": "Switching branch…",
  "Switch to branch": "Switch to branch",
  "Check out as a local branch": "Check out as a local branch",
  "Rebase {branch} onto this": "Rebase {branch} onto this",
  "Merging…": "Merging…",
  "Rebasing…": "Rebasing…",
  "Delete…": "Delete…",
  "Force delete…": "Force delete…",
  "Delete": "Delete",
  "Deleting branch…": "Deleting branch…",
  "Copy name": "Copy name",
  "That branch already exists.": "That branch already exists.",
  "Enter a name.": "Enter a name.",
  "The branch starts at the current commit, and your changes come along.": "The branch starts at the current commit, and your changes come along.",
  "The branch starts at {hash}.": "The branch starts at {hash}.",
  "Delete branch {name}?": "Delete branch {name}?",
  "A branch not merged anywhere is refused; delete it anyway to lose its commits.": "A branch not merged anywhere is refused; delete it anyway to lose its commits.",
  "Its commits are lost unless another branch has them.": "Its commits are lost unless another branch has them.",

  // Stashes
  "Message": "Message",
  "Stash": "Stash",
  "Stashing…": "Stashing…",
  "Every change, new files too, is put aside and the work tree left clean.": "Every change, new files too, is put aside and the work tree left clean.",
  "Pop": "Pop",
  "Popping…": "Popping…",
  "Apply the stash and drop it": "Apply the stash and drop it",
  "Apply": "Apply",
  "Applying…": "Applying…",
  "Drop…": "Drop…",
  "Drop": "Drop",
  "Dropping…": "Dropping…",
  "Stash actions": "Stash actions",
  "Drop this stash?": "Drop this stash?",
  "Its changes are lost.": "Its changes are lost.",

  // Discard confirm
  "Discard changes to {what}?": "Discard changes to {what}?",
  "The changes are lost, and new files deleted. This cannot be undone.": "The changes are lost, and new files deleted. This cannot be undone.",
  "Discard": "Discard",
  "Discarding…": "Discarding…",

  // Operations & conflicts
  "A merge": "A merge",
  "A rebase": "A rebase",
  "A cherry-pick": "A cherry-pick",
  "A revert": "A revert",
  "{what} is under way.": "{what} is under way.",
  "{what} stopped on 1 conflict.": "{what} stopped on 1 conflict.",
  "{what} stopped on {count} conflicts.": "{what} stopped on {count} conflicts.",
  "Resolve them, then continue, or abort.": "Resolve them, then continue, or abort.",
  "{what} stopped on {count} conflicts. Resolve them, then continue.": "{what} stopped on {count} conflicts. Resolve them, then continue.",
  "Abort": "Abort",
  "Aborting…": "Aborting…",
  "Abort the operation?": "Abort the operation?",
  "The work tree goes back to where it was before it began.": "The work tree goes back to where it was before it began.",
  "Continue": "Continue",
  "Continuing…": "Continuing…",
  "Mark the conflicts resolved?": "Mark the conflicts resolved?",
  "1 file is still marked as conflicted. Continuing takes it as it is now.": "1 file is still marked as conflicted. Continuing takes it as it is now.",
  "{count} files are still marked as conflicted. Continuing takes them as they are now.": "{count} files are still marked as conflicted. Continuing takes them as they are now.",

  // Worktree busy labels
  "Opening a pull request…": "Opening a pull request…",
  "Removing the worktree…": "Removing the worktree…",

  // History & commit detail
  "History divider": "History divider",
  "Commit divider": "Commit divider",
  "Showing the newest {count} commits.": "Showing the newest {count} commits.",
  "Check out": "Check out",
  "Checking out…": "Checking out…",
  "Check out {hash}?": "Check out {hash}?",
  "The work tree leaves its branch for this commit, on no branch. Changes that conflict stop it.": "The work tree leaves its branch for this commit, on no branch. Changes that conflict stop it.",
  "Create branch here…": "Create branch here…",
  "Create tag…": "Create tag…",
  "New tag": "New tag",
  "Tag name": "Tag name",
  "Create tag": "Create tag",
  "Tagging…": "Tagging…",
  "The tag marks {hash}.": "The tag marks {hash}.",
  "Cherry-pick": "Cherry-pick",
  "Cherry-picking…": "Cherry-picking…",
  "Revert": "Revert",
  "Reverting…": "Reverting…",
  "Copy hash": "Copy hash",
  "Copy subject": "Copy subject",
  "Close commit": "Close commit",

  // Forge line & Auto-fix
  "Running": "Running",
  "Failed": "Failed",
  "Passed": "Passed",
  "Actions": "Actions",
  "{count} runs of {head}": "{count} runs of {head}",
  "Auto-fix": "Auto-fix",
  "On": "On",
  "Working": "Working",
  "Paused": "Paused",
  "Error": "Error",
  "Reset": "Reset",
  "Reset attempts": "Reset attempts",
  "Save": "Save",
  "Session": "Session",
  "Fix now": "Fix now",
  "Auto-fix settings": "Auto-fix settings",
  "Turn off Auto-fix": "Turn off Auto-fix",
  "Open the Auto-fix session": "Open the Auto-fix session",
  "Start the attempts again": "Start the attempts again",
  "Fix failing checks and review comments automatically": "Fix failing checks and review comments automatically",
  "Auto-fix: on": "Auto-fix: on",
  "Auto-fix: watching CI and reviews": "Auto-fix: watching CI and reviews",
  "Auto-fix: watching CI": "Auto-fix: watching CI",
  "Auto-fix: watching reviews": "Auto-fix: watching reviews",
  "Auto-fix: starting an agent": "Auto-fix: starting an agent",
  "Auto-fix: fixing CI ({used}/{max})": "Auto-fix: fixing CI ({used}/{max})",
  "Auto-fix: addressing review comments": "Auto-fix: addressing review comments",
  "Auto-fix: waiting for CI": "Auto-fix: waiting for CI",
  "Auto-fix: waiting for the agent to finish its turn": "Auto-fix: waiting for the agent to finish its turn",
  "Auto-fix: CI fixes paused ({used}/{max})": "Auto-fix: CI fixes paused ({used}/{max})",
  "Auto-fix: needs attention": "Auto-fix: needs attention",
  "Last sent: {summary}": "Last sent: {summary}",
  "Auto-fix PR #{number}": "Auto-fix PR #{number}",
  "Auto-fix this pull request": "Auto-fix this pull request",
  "While the pull request is open, a check that fails or a new review comment goes to the agent session in this folder, or to a new agent. The agent pushes clear fixes and asks you about unclear comments. Mako checks while the Git panel is open.": "While the pull request is open, a check that fails or a new review comment goes to the agent session in this folder, or to a new agent. The agent pushes clear fixes and asks you about unclear comments. Mako checks while the Git panel is open.",
  "Fix failing CI checks": "Fix failing CI checks",
  "Address review comments": "Address review comments",
  "Reply to addressed comments on GitHub": "Reply to addressed comments on GitHub",
  "CI fix attempts in a row": "CI fix attempts in a row",
  "Agent for a new session": "Agent for a new session",
  "Auto (default agent)": "Auto (default agent)",
  "Custom instructions": "Custom instructions",
  "For example: only fix lint and test failures, and run the tests before you push.": "For example: only fix lint and test failures, and run the tests before you push.",
  "Recent activity": "Recent activity",
  "Auto-fix sent a fix for PR #{number}.": "Auto-fix sent a fix for PR #{number}.",
  "Checks pass and no review comments are open.": "Checks pass and no review comments are open.",
  "Auto-fix is already checking this pull request.": "Auto-fix is already checking this pull request.",

  // Relative time
  "now": "now",
  "{count}m": "{count}m",
  "{count}h": "{count}h",
  "{count}d": "{count}d",

  // Disclosure accessibility
  "Expand {title}": "Expand {title}",
  "Collapse {title}": "Collapse {title}",

  // HTTPS authentication
  "HTTPS authentication": "HTTPS authentication",
  "Authenticate…": "Authenticate…",
  "Sign in & retry": "Sign in & retry",
  "Username (optional)": "Username (optional)",
  "Password or token": "Password or token",
  "Personal access token or password": "Personal access token or password",
  "Save in Git credential store": "Save in Git credential store",
  "Enter a password or personal access token.": "Enter a password or personal access token.",
  "Leave username blank to use a personal access token. Credentials are not written to .git/config.": "Leave username blank to use a personal access token. Credentials are not written to .git/config.",
};

const ko = {
  "Git": "Git",
  "Changes": "변경 사항",
  "Stashes": "스태시",
  "Branches": "브랜치",
  "History": "히스토리",
  "Remote": "원격",
  "Refresh": "새로고침",
  "Loading…": "불러오는 중…",
  "Dismiss": "닫기",
  "Cancel": "취소",
  "Optional": "선택 사항",

  // Empty & error states
  "Open a session to see its changes.": "세션을 열면 그 프로젝트의 변경 사항을 볼 수 있습니다.",
  "Not a git repository.": "git 저장소가 아닙니다.",
  "No changes.": "변경 사항이 없습니다.",
  "No stashes.": "스태시가 없습니다.",
  "No commits yet.": "아직 커밋이 없습니다.",
  "No files changed.": "바뀐 파일이 없습니다.",
  "binary": "바이너리",

  // Toolbar & sync
  "Branch": "브랜치",
  "Git actions": "Git 작업",
  "vs HEAD": "HEAD 대비",
  "since {base}": "{base} 이후",
  "Compare with HEAD, or with the branch the worktree started from": "HEAD와 비교하거나, worktree가 시작된 브랜치와 비교합니다",
  "Fetch": "가져오기(Fetch)",
  "Fetching…": "가져오는 중…",
  "Pull": "풀",
  "Pull (rebase)": "Pull (rebase)",
  "Pull from {upstream}": "{upstream}에서 풀",
  "Pulling…": "풀 하는 중…",
  "Push": "푸시",
  "Push to {upstream}": "{upstream}로 푸시",
  "Pushing…": "푸시하는 중…",
  "Create pull request": "풀 리퀘스트 만들기",
  "Create pull request…": "풀 리퀘스트 만들기…",
  "New branch": "새 브랜치",
  "New branch…": "새 브랜치…",
  "Stash changes": "변경 사항 스태시",
  "Stash changes…": "변경 사항 스태시…",
  "Discard all changes…": "모든 변경 사항 버리기…",
  "Merge into {branch}": "{branch}에 병합",
  "Merge into the base branch": "기준 브랜치에 병합",
  "Open a pull request": "Pull request 열기",
  "Remove worktree": "worktree 제거",

  // Changes & commit box
  "Commit {name}": "{name} 커밋에 포함",
  "Commit message": "커밋 메시지",
  "Enter a commit message.": "커밋 메시지를 입력하세요.",
  "Amend": "이전 커밋 수정",
  "Amend last commit": "마지막 커밋 수정",
  "Commit 1 file": "파일 1개 커밋",
  "Commit {count} files": "파일 {count}개 커밋",
  "Committing…": "커밋하는 중…",
  "Open diff": "diff 열기",
  "Open file": "파일 열기",
  "Discard changes…": "변경 사항 버리기…",
  "Add to .gitignore": ".gitignore에 추가",
  "Ignoring…": "무시 목록에 추가하는 중…",
  "Copy path": "경로 복사",
  "{count} files": "파일 {count}개",

  // Branches
  "Branch name": "브랜치 이름",
  "Create branch": "브랜치 만들기",
  "Making branch…": "브랜치 만드는 중…",
  "Switching branch…": "브랜치 바꾸는 중…",
  "Switch to branch": "이 브랜치로 전환",
  "Check out as a local branch": "로컬 브랜치로 체크아웃",
  "Rebase {branch} onto this": "{branch}을(를) 이 브랜치 위로 rebase",
  "Merging…": "병합하는 중…",
  "Rebasing…": "rebase하는 중…",
  "Delete…": "삭제…",
  "Force delete…": "강제 삭제…",
  "Delete": "삭제",
  "Deleting branch…": "브랜치 삭제 중…",
  "Copy name": "이름 복사",
  "That branch already exists.": "이미 있는 브랜치입니다.",
  "Enter a name.": "이름을 입력하세요.",
  "The branch starts at the current commit, and your changes come along.": "현재 커밋에서 브랜치를 만들고, 작업 중인 변경은 그대로 따라옵니다.",
  "The branch starts at {hash}.": "브랜치는 {hash}에서 시작합니다.",
  "Delete branch {name}?": "{name} 브랜치를 삭제할까요?",
  "A branch not merged anywhere is refused; delete it anyway to lose its commits.": "어디에도 병합되지 않은 브랜치는 거부됩니다. 그래도 지우려면 강제 삭제하세요(커밋이 사라집니다).",
  "Its commits are lost unless another branch has them.": "다른 브랜치에 없는 커밋은 사라집니다.",

  // Stashes
  "Message": "메시지",
  "Stash": "스태시",
  "Stashing…": "스태시하는 중…",
  "Every change, new files too, is put aside and the work tree left clean.": "새 파일을 포함한 모든 변경 사항을 따로 보관하고 작업 트리를 깨끗하게 만듭니다.",
  "Pop": "꺼내기",
  "Popping…": "꺼내는 중…",
  "Apply the stash and drop it": "스태시를 적용하고 버립니다",
  "Apply": "적용",
  "Applying…": "적용하는 중…",
  "Drop…": "버리기…",
  "Drop": "버리기",
  "Dropping…": "버리는 중…",
  "Stash actions": "스태시 작업",
  "Drop this stash?": "이 스태시를 버릴까요?",
  "Its changes are lost.": "변경 사항이 사라집니다.",

  // Discard confirm
  "Discard changes to {what}?": "{what}의 변경 사항을 버릴까요?",
  "The changes are lost, and new files deleted. This cannot be undone.": "변경 사항이 사라지고 새 파일은 삭제됩니다. 되돌릴 수 없습니다.",
  "Discard": "버리기",
  "Discarding…": "버리는 중…",

  // Operations & conflicts
  "A merge": "병합",
  "A rebase": "rebase",
  "A cherry-pick": "체리픽",
  "A revert": "되돌리기",
  "{what} is under way.": "{what} 진행 중입니다.",
  "{what} stopped on 1 conflict.": "{what} 중 충돌 1개로 멈췄습니다.",
  "{what} stopped on {count} conflicts.": "{what} 중 충돌 {count}개로 멈췄습니다.",
  "Resolve them, then continue, or abort.": "충돌을 해결한 뒤 계속하거나 중단하세요.",
  "{what} stopped on {count} conflicts. Resolve them, then continue.": "{what} 중 충돌 {count}개로 멈췄습니다. 해결한 뒤 계속하세요.",
  "Abort": "중단",
  "Aborting…": "중단하는 중…",
  "Abort the operation?": "작업을 중단할까요?",
  "The work tree goes back to where it was before it began.": "작업 트리가 시작하기 전 상태로 돌아갑니다.",
  "Continue": "계속",
  "Continuing…": "계속하는 중…",
  "Mark the conflicts resolved?": "충돌을 해결된 것으로 표시할까요?",
  "1 file is still marked as conflicted. Continuing takes it as it is now.": "파일 1개가 아직 충돌로 표시되어 있습니다. 계속하면 지금 상태 그대로 반영됩니다.",
  "{count} files are still marked as conflicted. Continuing takes them as they are now.": "파일 {count}개가 아직 충돌로 표시되어 있습니다. 계속하면 지금 상태 그대로 반영됩니다.",

  // Worktree busy labels
  "Opening a pull request…": "Pull request 여는 중…",
  "Removing the worktree…": "worktree 제거하는 중…",

  // History & commit detail
  "History divider": "히스토리 구분선",
  "Commit divider": "커밋 구분선",
  "Showing the newest {count} commits.": "최근 커밋 {count}개를 보여줍니다.",
  "Check out": "체크아웃",
  "Checking out…": "체크아웃하는 중…",
  "Check out {hash}?": "{hash}을(를) 체크아웃할까요?",
  "The work tree leaves its branch for this commit, on no branch. Changes that conflict stop it.": "작업 트리가 브랜치를 떠나 이 커밋으로 이동합니다(브랜치 없음). 충돌하는 변경이 있으면 멈춥니다.",
  "Create branch here…": "여기서 브랜치 만들기…",
  "Create tag…": "태그 만들기…",
  "New tag": "새 태그",
  "Tag name": "태그 이름",
  "Create tag": "태그 만들기",
  "Tagging…": "태그 다는 중…",
  "The tag marks {hash}.": "태그가 {hash}을(를) 가리킵니다.",
  "Cherry-pick": "체리픽",
  "Cherry-picking…": "체리픽하는 중…",
  "Revert": "되돌리기",
  "Reverting…": "되돌리는 중…",
  "Copy hash": "해시 복사",
  "Copy subject": "제목 복사",
  "Close commit": "커밋 닫기",

  // Forge line & Auto-fix
  "Running": "실행 중",
  "Failed": "실패",
  "Passed": "통과",
  "Actions": "Actions",
  "{count} runs of {head}": "{head}의 실행 {count}개",
  "Auto-fix": "자동 수정",
  "On": "켜짐",
  "Working": "작업 중",
  "Paused": "일시 중지",
  "Error": "오류",
  "Reset": "초기화",
  "Reset attempts": "시도 횟수 초기화",
  "Save": "저장",
  "Session": "세션",
  "Fix now": "지금 수정",
  "Auto-fix settings": "자동 수정 설정",
  "Turn off Auto-fix": "자동 수정 끄기",
  "Open the Auto-fix session": "자동 수정 세션 열기",
  "Start the attempts again": "시도 횟수를 처음부터 다시 셉니다",
  "Fix failing checks and review comments automatically": "실패한 체크와 리뷰 코멘트를 자동으로 수정합니다",
  "Auto-fix: on": "자동 수정: 켜짐",
  "Auto-fix: watching CI and reviews": "자동 수정: CI와 리뷰 감시 중",
  "Auto-fix: watching CI": "자동 수정: CI 감시 중",
  "Auto-fix: watching reviews": "자동 수정: 리뷰 감시 중",
  "Auto-fix: starting an agent": "자동 수정: 에이전트 시작 중",
  "Auto-fix: fixing CI ({used}/{max})": "자동 수정: CI 수정 중 ({used}/{max})",
  "Auto-fix: addressing review comments": "자동 수정: 리뷰 코멘트 반영 중",
  "Auto-fix: waiting for CI": "자동 수정: CI 대기 중",
  "Auto-fix: waiting for the agent to finish its turn": "자동 수정: 에이전트가 하던 작업을 마치기를 기다리는 중",
  "Auto-fix: CI fixes paused ({used}/{max})": "자동 수정: CI 수정 일시 중지 ({used}/{max})",
  "Auto-fix: needs attention": "자동 수정: 확인 필요",
  "Last sent: {summary}": "최근 요청: {summary}",
  "Auto-fix PR #{number}": "PR #{number} 자동 수정",
  "Auto-fix this pull request": "이 풀 리퀘스트 자동 수정",
  "While the pull request is open, a check that fails or a new review comment goes to the agent session in this folder, or to a new agent. The agent pushes clear fixes and asks you about unclear comments. Mako checks while the Git panel is open.": "풀 리퀘스트가 열려 있는 동안 실패한 체크나 새 리뷰 코멘트를 이 폴더의 에이전트 세션에 보내고, 세션이 없으면 새 에이전트를 시작합니다. 에이전트는 명확한 수정은 푸시하고, 모호한 코멘트는 사용자에게 묻습니다. Mako는 Git 패널이 열려 있는 동안 확인합니다.",
  "Fix failing CI checks": "실패한 CI 체크 수정",
  "Address review comments": "리뷰 코멘트 반영",
  "Reply to addressed comments on GitHub": "반영한 코멘트에 GitHub 답글 달기",
  "CI fix attempts in a row": "연속 CI 수정 시도 횟수",
  "Agent for a new session": "새 세션에 쓸 에이전트",
  "Auto (default agent)": "자동 (기본 에이전트)",
  "Custom instructions": "추가 지침",
  "For example: only fix lint and test failures, and run the tests before you push.": "예: 린트와 테스트 실패만 고치고, 푸시하기 전에 테스트를 실행해 줘.",
  "Recent activity": "최근 활동",
  "Auto-fix sent a fix for PR #{number}.": "PR #{number}에 자동 수정을 요청했습니다.",
  "Checks pass and no review comments are open.": "모든 체크가 통과했고 처리할 리뷰 코멘트가 없습니다.",
  "Auto-fix is already checking this pull request.": "이 풀 리퀘스트를 이미 확인하는 중입니다.",

  // Relative time
  "now": "방금",
  "{count}m": "{count}분",
  "{count}h": "{count}시간",
  "{count}d": "{count}일",

  // Disclosure accessibility
  "Expand {title}": "{title} 펼치기",
  "Collapse {title}": "{title} 접기",

  // HTTPS authentication
  "HTTPS authentication": "HTTPS 인증",
  "Authenticate…": "인증…",
  "Sign in & retry": "인증 후 재시도",
  "Username (optional)": "사용자 이름 (선택)",
  "Password or token": "비밀번호 또는 토큰",
  "Personal access token or password": "개인 액세스 토큰 또는 비밀번호",
  "Save in Git credential store": "Git 자격 증명 저장소에 저장",
  "Enter a password or personal access token.": "비밀번호 또는 개인 액세스 토큰을 입력하세요.",
  "Leave username blank to use a personal access token. Credentials are not written to .git/config.": "개인 액세스 토큰을 사용할 때는 사용자 이름을 비워 두세요. 자격 증명은 .git/config에 기록되지 않습니다.",
};

export const catalog = { en, ko };

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

export function shortHash(h) {
  const s = String(h || "");
  return s.length > 7 ? s.slice(0, 7) : s;
}

export function lineCount(n) {
  const v = Number(n) || 0;
  if (v < 1000) return String(v);
  const s = (v / 1000).toFixed(1);
  return (s.endsWith(".0") ? s.slice(0, -2) : s) + "k";
}

function pad2(n) {
  return n < 10 ? "0" + n : String(n);
}

export function ago(rfc3339, t) {
  if (!rfc3339) return "";
  const ms = Date.parse(rfc3339);
  if (!Number.isFinite(ms)) return "";
  const diffSec = Math.floor((Date.now() - ms) / 1000);
  if (diffSec < 60) return t("now");
  const mins = Math.floor(diffSec / 60);
  if (mins < 60) return t("{count}m", { count: mins });
  const hours = Math.floor(mins / 60);
  if (hours < 24) return t("{count}h", { count: hours });
  const days = Math.floor(hours / 24);
  if (days < 30) return t("{count}d", { count: days });
  const d = new Date(ms);
  if (days < 365) return `${MONTHS[d.getMonth()]} ${d.getDate()}`;
  return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}`;
}

export function formatDateTime(rfc3339) {
  if (!rfc3339) return "";
  const ms = Date.parse(rfc3339);
  if (!Number.isFinite(ms)) return String(rfc3339);
  const d = new Date(ms);
  return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())} ${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
}

export function lastLines(text, n) {
  const trimmed = String(text || "").trim();
  if (!trimmed) return "";
  const lines = trimmed.split(/\r?\n/);
  return lines.slice(Math.max(0, lines.length - n)).join("\n");
}
