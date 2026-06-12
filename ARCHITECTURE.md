# AgentDash — Architecture

> A read-only terminal dashboard that monitors **all** local AI coding agents from
> one place — Claude Code, Codex, opencode, Copilot CLI, Kimi, and more — and
> correlates each running agent with the **git worktree** it is working in to show
> not just *"is the agent busy?"* but *"what work has it produced, and can it be
> merged?"*

---

## 1. Positioning & differentiator

AgentDash is an **observer**, not a launcher: it never spawns agents (no tmux
panes, no orchestration). You start your agents however you like; AgentDash reads
their state and the git worktrees they work in.

**The core design:** AgentDash is a *correlation engine*. Two independent
collectors — one watching each agent's own session files, one scanning git
worktrees — are **joined on the filesystem path** (`agent.cwd` ↔ `worktree.path`).
That join lets AgentDash display, per session, both the live agent activity
**and** the concrete git outcome: diff stat, commits, ahead/behind,
**mergeability**, and **cross-worktree file collisions** — two agents about to
edit the same file are flagged before it becomes a merge conflict.

Storage is deliberately minimal: flat JSON files plus a config YAML, no daemon and
no database.

---

## 2. Language & stack — **Go**

- **TUI:** Bubble Tea (`charmbracelet/bubbletea`) + Bubbles (list/viewport/help) +
  Lip Gloss (layout/style). Best-in-class for a polished terminal aesthetic.
- **CLI:** Cobra.
- **Config:** YAML (`~/.agentdash/config.yaml`).
- **State:** flat JSON files (no daemon, no DB) per the MVP constraint.
- **Concurrency:** goroutines for the parallel collectors; results merged on the
  Bubble Tea event loop via messages (the UI model is never mutated off-thread).

Rationale: workload is I/O-bound (read JSONL, shell out to git, scan processes,
redraw). Rust's strengths don't apply here; Bubble Tea has no Rust equal for this
look. Every reference tool is Go.

---

## 3. The core architecture: a correlation engine

```
                         ┌─────────────────────────────────────────┐
                         │            poll loop (tea.Tick, 2s)      │
                         └─────────────────────────────────────────┘
                                          │ triggers
              ┌───────────────────────────┴───────────────────────────┐
              ▼                                                         ▼
   ┌──────────────────────┐                              ┌──────────────────────────┐
   │  Agent collector     │                              │  Worktree collector      │
   │  (per-agent adapters)│                              │  (git, agent-agnostic)   │
   ├──────────────────────┤                              ├──────────────────────────┤
   │ Claude  → ~/.claude  │                              │ for each watched repo:   │
   │ Codex   → ~/.codex   │                              │   git worktree list      │
   │ opencode→ ~/.local…  │  ── AgentSession{cwd,…} ─┐    │   branch / diff --stat   │
   │ Copilot → ~/.copilot │                          │   │   log base..HEAD         │
   │ Kimi    → ~/.kimi-…  │                          │   │   rev-list ahead/behind  │
   │ (generic process scan│                          │   │   merge-tree (mergeable) │
   │  for unknown agents) │                          │   └──────────┬───────────────┘
   └──────────────────────┘                          │              │ Worktree{path,…}
                                                      ▼              ▼
                                   ┌───────────────────────────────────────────┐
                                   │           Correlator                       │
                                   │   join on  agent.cwd  ↔  worktree.path     │
                                   │   + cross-worktree collision detection     │
                                   └───────────────────┬───────────────────────┘
                                                       │ []Session (unified)
                                                       ▼
                                   ┌───────────────────────────────────────────┐
                                   │   Bubble Tea model  →  render 3 panels     │
                                   └───────────────────────────────────────────┘
```

### 3.1 Agent collector — pluggable adapters

```go
// internal/agent/adapter.go
type AgentSession struct {
    Agent       string    // "claude" | "codex" | "opencode" | "copilot" | "kimi" | "unknown"
    SessionID   string
    CWD         string    // ← THE JOIN KEY (absolute, symlink-resolved)
    Model       string    // if recorded
    GitBranch   string    // if the agent records it (Claude/Codex do)
    LastActive  time.Time // file mtime or last-event timestamp
    Activity    Activity  // Working | Waiting | Idle | Unknown
    PID         int       // if matched via process scan (0 = unknown)
    SourceFile  string    // path to the transcript we read
}

type AgentAdapter interface {
    Name() string
    // Discover returns every session this adapter can see right now.
    // Cheap: stat/scan dirs, read only the head/tail of files. No full parse.
    Discover() ([]AgentSession, error)
}
```

- **MVP adapters:** `claude`, `codex`, `opencode`, `copilot`, `kimi`.
- **Generic fallback:** `procscan` adapter — scans for known process names
  (`claude`, `codex`, `cursor`, `copilot`, `kimi`, `grok`, `opencode`, …) and
  reads each PID's `cwd`. Yields `Activity=Unknown` but guarantees *every* agent
  appears even without a transcript adapter (graceful degradation).
- Adapters are **registered**, so adding `cursor`/`grok` later is one file, no core
  change.

**Verified session locations (env-override aware — NO hardcoded absolute paths):**

| Adapter | Root (env override) | Layout | cwd source | Activity source |
| --- | --- | --- | --- | --- |
| claude | `~/.claude` | `projects/<slug>/<id>.jsonl` | `cwd` field per line | tail line `type` + mtime |
| codex | `~/.codex` (`CODEX_HOME`) | `sessions/Y/M/D/rollout-*.jsonl` | `SessionMeta.cwd` | mtime |
| opencode | `~/.local/share/opencode` (`OPENCODE_DATA_DIR`) | `…/storage/session/*.json` | project-slug dir | mtime |
| copilot | `~/.copilot` (`COPILOT_HOME`) | `session-state/<id>/{events.jsonl,workspace.yaml}` | `workspace.yaml` | events.jsonl tail + mtime |
| kimi | `~/.kimi-code` (`KIMI_CODE_HOME`) | `sessions/` + `session_index.jsonl` | index / `user-history/<md5(workDir)>` | mtime |

> **Reverse-engineering risk** is isolated entirely inside each adapter. A format
> change breaks one adapter, not the dashboard. Adapters fail soft: an error from
> one is logged and skipped; the rest of the dashboard still renders.

**Activity classification (per agent):** read only the *tail* of the transcript.
`Working` = last event is an in-progress assistant/tool turn and mtime < ~10s;
`Waiting` = last event awaits user input; `Idle` = alive but stale; `Unknown` =
process-scan only. Status glyphs: `● ◐ ○ ✕`.

### 3.2 Worktree collector — agent-agnostic git truth

For each repo in `config.repos`:
- `git worktree list --porcelain` → one candidate worktree each.
- Per worktree, shelled out with `git -C <path>`:
  - `branch --show-current`
  - `status --short` (changed-file set — also feeds collision detection)
  - `diff --stat` and `diff --cached --stat`
  - `log <base>..HEAD --oneline` (commits since base)
  - `rev-list --left-right --count <base>...HEAD` (ahead/behind)
  - `merge-tree --write-tree <base> HEAD` → **mergeable? / conflict files** (does
    not touch the working tree)
- Base branch: auto-detect `origin/HEAD` → `main`/`master`, per-repo override in config.

### 3.3 Correlator — the join + the moat

1. **Join:** index worktrees by resolved absolute path. For each `AgentSession`,
   walk up `CWD` until it matches a worktree path (an agent may run in a subdir of
   the worktree). Produce unified `Session` = agent half + git half. Worktrees
   with no agent still appear (status `no agent`); agents outside any watched repo
   appear under an "unmatched" group.
2. **Cross-worktree collision detection (headline feature):** intersect the
   changed-file sets across *all* active worktrees. If worktree A and worktree B
   both touch `auth.go`, both are flagged `⚠ collides: session-B`. This is derived
   state across the whole fleet — native to AgentDash's design, awkward for
   per-session monitors to bolt on.

```go
// internal/session/session.go — the unified row the TUI renders
type Session struct {
    // identity / agent half
    Agent, SessionID, Model string
    Activity                Activity
    PID                     int
    LastActive              time.Time
    // git half
    RepoPath, WorktreePath, Branch, Base string
    Task                                  string // from optional JSON overlay only
    ChangedFiles                          int
    Insertions, Deletions                int
    CommitsSinceBase                     int
    Ahead, Behind                        int
    Mergeable                            bool
    ConflictFilesWithBase                []string
    CollidesWith                         []string // other sessions touching same files
    Notes                                string
}
```

### 3.4 Optional JSON overlay (the one thing git/transcripts can't give)

`Task` and `Notes` aren't observable. AgentDash reads an **optional** overlay from
`~/.agentdash/sessions/<id>.json` (the schema from the original spec) and merges it
in if present, keyed by worktree path or session id. This is *enrichment*, never
required — a session shows up purely from being observed.

---

## 4. TUI layout (session list + detail deck)

```
┌─ Sessions ───────────┐┌─ Detail ──────────────────────────────────┐
│ ● claude   feat/auth ││ agent   claude   model  opus-4.8           │
│ ◐ codex    fix/race  ││ repo    ~/proj   branch feat/auth (→main)  │
│ ○ kimi     docs      ││ task    "wire up oauth"   pid 41233 alive  │
│ ⚠ copilot  feat/api  ││ ahead 3  behind 0   mergeable ✓            │
│   (collides: codex)  │├─ Git ──────────────────────────────────────┤
│ ✕ opencode exited    ││ 12 files  +418 -57    churn ▁▂▄▆█▆▃        │
│                      ││ M internal/auth/oauth.go                   │
│ [unmatched]          ││ A internal/auth/token.go                   │
│ ● grok    (no repo)  │├─ Activity / Collisions ────────────────────┤
│                      ││ ⚠ collides with codex on internal/util.go  │
│                      ││ last commit  a1b2c3d  "add token store"    │
└──────────────────────┘└────────────────────────────────────────────┘
 q quit  r refresh  ↵ detail  d diff  s status  c conflicts  o open  ? help
```

- **Left:** session list (Bubbles `list`), status glyph + agent + branch; collision
  badge inline. Sorted by activity then churn.
- **Right top:** session detail deck.
- **Right middle:** git status + diff stat + **churn sparkline** (rolling history).
- **Right bottom:** collisions + commits/activity.
- **Keys:** `q r ↵ d s c o ?` — `c` = conflict/collision report (new), `o` = open
  worktree in `$EDITOR`.

---

## 5. Package layout

```
/cmd/agentdash         cobra root → launches TUI; `init`; optional `worktree new`
/internal/config       load/write ~/.agentdash/config.yaml (repos, bases, agents)
/internal/agent        AgentAdapter interface + registry
  /internal/agent/claude   /codex  /opencode  /copilot  /kimi   /procscan
/internal/gitutil      git command wrappers + porcelain parsers
/internal/worktree     worktree collector (uses gitutil)
/internal/correlate    join (cwd↔path) + cross-worktree collision detection
/internal/activity     transcript-tail → Activity classification
/internal/history      churn timeline persistence (~/.agentdash/history)
/internal/session      unified Session model + optional JSON overlay load/save
/internal/tui          Bubble Tea model, panels, 2s tick, keybindings
```

No `run`-that-spawns-an-agent command. An optional **thin** `worktree new` helper
(create worktree + branch + write overlay JSON) is the only write path, and it
never launches an agent — preserving the monitor-only contract.

---

## 6. The 2-second refresh, safely

`tea.Tick(2s)` emits `refreshMsg`. On it, `Update` fires a `tea.Cmd` that runs both
collectors **in a goroutine** (off the UI thread), and returns a `sessionsMsg` with
the merged `[]Session`. The model swaps state on that message only. Git/transcript
work never blocks rendering. A single in-flight guard prevents overlap if a poll
runs long.

---

## 7. MVP build order

1. `config` + `agentdash init` → write `~/.agentdash/config.yaml` (+ `sessions/`, `history/`).
2. `gitutil` + `worktree` → list worktrees, compute branch/diff/log/ahead-behind. **Unit-tested parsers.**
3. `agent` registry + **claude** adapter (best-documented) + `procscan` fallback.
4. `correlate` join → unified sessions.
5. `tui` skeleton: list + detail + git panels + 2s tick. **First demo: real sessions correlated to worktrees.**
6. Remaining adapters: `codex`, `opencode`, `copilot`, `kimi`.
7. `correlate` collision detection + `c` panel (**headline**).
8. `activity` classification + `history` sparkline (texture).
9. README crediting prior art; tests for gitutil parsing, correlation join, collision set-intersection, overlay load/save, slug generation.

## 8. Acceptance criteria (revised for monitor-only)

1. `agentdash init` creates config + dirs, clear errors. ✓
2. Dashboard discovers running agents across the 5 supported tools **without launching them**. ✓
3. Each session shows agent, model, repo, worktree, branch, activity, changed files, diff stat, ahead/behind. ✓
4. A worktree with no live agent still appears (git truth); an agent outside watched repos still appears (unmatched). ✓
5. When an agent process exits, activity flips to `exited`/`idle` within one poll. ✓
6. Two agents touching the same file are flagged as colliding. ✓
7. No hardcoded absolute paths; all agent roots honor env overrides. ✓
8. Works on macOS and Linux. ✓
```
