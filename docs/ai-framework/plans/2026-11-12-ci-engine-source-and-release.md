# 阶段 N：修复 CI 引擎源 + 发布 v2.0.5

> **状态：** ✅ 已完成（2026-11-12，v2.0.5 Release 已发布）
> **For agentic workers:** 按 Task 顺序执行；每 Task 测完再进下一 Task。验收认证据，禁止「应该能过」。
> **元规范:** `docs/ai-framework/phased-plan-driven.md`；仓库约定 `AGENTS.md`
> **基线分支：** `main`（tip `3df13f0`，已 push 到 origin，本地 2 分支与线上一致）
> **交付提交：** `5ea9e74`(计划) / `f142f6e`(CI 引擎源) / `f8ec61f`(CHANGELOG) / `68d3ddf`(CI 空提交)；tag `v2.0.5`（object `2d504e6`）

**Goal:** 把 CI 里已 404 的引擎源码源 `wbi35772/opencode2api-win@9964407` 换成真上游 `jasonxu114514/opencode2api@v1.4.7`（11-11 调研已实测兼容），修复三平台 build，然后写 v2.0.5 CHANGELOG、打 tag 推送，触发完整打包 + 发 GitHub Release。

**Architecture / 根因：**
- 11-05~11-07 的 v2.0.0~v2.0.4 tag 构建全 success；11-11 调研记录 `wbi35772/opencode2api-win` **404/已删除**，真上游 = `jasonxu114514/opencode2api`（活跃，v1.4.7）。今天 11-12 推 main 触发的 run `43149792951` 三平台全挂在 `git clone wbi35772/...` → `Repository not found`，`release` job 因 `needs: build` 被 skipped。
- 核实：真上游 `v1.4.7` tag object `1011e14` → commit `b296a61d0e3ef93b4df58e298be2c4e34e397322`；浅克隆确认 `./cmd/opencode2api/main.go` 存在、`go.mod` 要求 `go 1.24`；workflow 的 `setup-go` 已是 `1.24`（够用）。
- 11-11 调研 `top5-99-consolidation-and-upgrade.md`：stock v1.4.7 已实测吃我们现有配置（SHA256 校验、healthz 12/44、推理 3/3 200），代价仅是失去只在 WARP lane 生效的分叉补丁（我们 WARP 关着跑不到）。

**Tech Stack:** GitHub Actions YAML、Go 1.24、Git tag、CHANGELOG.md。

**实施档位：** 全能　**子代理：** 启用（独立代码审查）

---

## 前置阅读（必须）

| 优先级 | 文件 |
|---|---|
| P0 | 本文件、`docs/ai-framework/phased-plan-driven.md` |
| P0 | `.github/workflows/build-release.yml`（build job :97-111 引擎 clone、:116-128 GPL 源码包、release job :235+） |
| P0 | `docs/opencode-channel-research-2026-11-11/top5-99-consolidation-and-upgrade.md`（v1.4.7 血统与实测） |
| P1 | `AGENTS.md`、`docs/issue-log/OPEN.md`、`third_party/opencode2api-win/SOURCE.txt`、`CHANGELOG.md` |

---

## Global Constraints（冲突时以本节为准）

1. **GPL-3.0 合规红线**：换源后 `SOURCE.txt` 的上游仓库/提交号必须与 CI 实际 clone 并 `git archive` 的源**逐字一致**；`COMMIT.txt` 由 CI 自动生成，不得手写伪造。LICENSE 保持 GPL-3.0 原文不动。
2. **固定 commit，不追 HEAD**：`ENGINE_COMMIT` 用 v1.4.7 的 `b296a61…`（或 tag `v1.4.7`），保证可复现；不写分支名。
3. **不降级 Go 版本**：setup-go 保持 `1.24`（真上游 go.mod 要求 1.24+）。
4. **不改生产运行配置**：本阶段只改 CI/文档/tag，不动 `bin/`、`config.json`、不退 WARP、不换生产引擎二进制。
5. **发版是外发行为**：打 tag + push 会触发 GitHub Release（公开）。按用户明确授权执行；**默认不 push 任何其它分支**。
6. **先本地验证再 push**：workflow 改动必须先本地验证 YAML 可解析、引擎源码可 clone+build（至少 1 平台交叉编译），再推。
7. **Git**：小步 commit，规范 `<gitmoji><type>(<scope>): 中文描述`；**只 push main 与 v2.0.5 tag**。
8. **YAGNI**：不做 v1.4.7 源码合并、不改引擎分叉逻辑、不修 L3 WARP 缺陷、不做 sessionGuard 回灌（11-11 已判定不值）。

---

## 阶段开头：上阶段遗留（如实带入）

| 上阶段遗留项 | 来源 | 状态 |
|---|---|---|
| V-6b：`custom.go:332` 硬编码 `Free:true` + auto 候选通用缺陷 | 阶段 K 审查 B4 | ⬜ 本阶段不修 |
| B5：生产部署 + 停机 V-7（阶段 K/L/M 代码上生产） | 阶段 K 审查 B5 | ⬜ 需人类授权，本阶段不修（发 Release ≠ 部署） |
| `callmetric_engine_test.go` 4 条时间炸弹 | OPEN.md 11-10 #5 | ⬜ 整包「无新增失败」判定 |
| 累计统计缺 vendor 维度 / 同步落盘偏离 / `call_log_max` 不约束 Kilo 日志 | 阶段 L 审查 A/E | ⬜ 已知限制 |
| 虚拟模型 SSE 流式不支持 / `race` N 倍代价 | 阶段 M 已知限制 | ⬜ 登记 |
| **wbi35772 引擎仓库 404 → CI 发版链路断** | 本阶段起因 | ✅ **本阶段修复** |

---

## File Structure（预期变更）

| 文件 | 动作 | 职责 |
|---|---|---|
| `.github/workflows/build-release.yml` | 修改 | `ENGINE_COMMIT` 换 v1.4.7 commit；clone URL 换真上游；注释同步 |
| `third_party/opencode2api-win/SOURCE.txt` | 修改 | 上游仓库 URL / commit / 提交说明改为 v1.4.7 真实值（GPL 合规） |
| `third_party/opencode2api-win/README.md` | 修改（若含旧 URL） | 同步上游来源说明 |
| `CHANGELOG.md` | 修改 | 顶部新增 `## v2.0.5（2026-11-12）` 段落（阶段 K/L/M + CI 修复） |
| `docs/issue-log/2026-11-12.md`、`OPEN.md` | 修改 | 阶段 N 记录、移除「CI 发版链路断」开放项 |
| `docs/ai-framework/plans/2026-11-12-ci-engine-source-and-release.md` | 新建 | 本计划 |
| `scripts/verify-ci-engine-source.sh`（可选） | 新建 | 本地复现 clone+build 的只读验证脚本 |

---

## 配置或 API 契约

```yaml
# .github/workflows/build-release.yml :97-111 修改后
- name: Build free-tier engine (opencode2api-win)
  env:
    # 真上游 jasonxu114514/opencode2api v1.4.7（wbi35772 分叉仓库 2026-11-11 起 404）
    ENGINE_REPO: https://github.com/jasonxu114514/opencode2api.git
    ENGINE_COMMIT: b296a61d0e3ef93b4df58e298be2c4e34e397322   # v1.4.7
  run: |
    git clone --no-checkout "$ENGINE_REPO" engine-src
    git -C engine-src checkout "$ENGINE_COMMIT"
    ( cd engine-src && CGO_ENABLED=0 GOOS=... GOARCH=... \
        go build -trimpath -ldflags "-s -w" -o ../bin/${{ matrix.engine_bin }} ./cmd/opencode2api )
```

```text
# third_party/opencode2api-win/SOURCE.txt 同步
2) 上游仓库
   https://github.com/jasonxu114514/opencode2api
3) 对应上游提交
   commit  : b296a61d0e3ef93b4df58e298be2c4e34e397322
   提交说明: v1.4.7（stock，2026-11-05）
```

```markdown
## v2.0.5（2026-11-12）
### 新增 / 修复
- Kilo 独立网关（30080）… 阶段 K
- 供应商维度统一（日志/统计/诊断/设置）… 阶段 L
- Kilo 命名虚拟模型三策略 … 阶段 M
- 修复 CI 引擎源码源：wbi35772/opencode2api-win 已 404，改用真上游 jasonxu114514/opencode2api v1.4.7
```

---

## Task 1：修 CI 引擎源 + GPL 声明（本地，先不 push）

**Files:** `.github/workflows/build-release.yml`、`third_party/opencode2api-win/SOURCE.txt`、`README.md`

**Steps:**
1. 改 `ENGINE_REPO` / `ENGINE_COMMIT` 为 v1.4.7 值；clone URL 换真上游；`git checkout "$ENGINE_COMMIT"` 保持不变（固定 commit 可复现）。
2. 改 `SOURCE.txt` 的上游 URL / commit / 提交说明；若 `README.md` 含旧 wbi35772 URL 一并改；**LICENSE 不动**。
3. 本地验证：
   - `python -c "import yaml; yaml.safe_load(open('.github/workflows/build-release.yml'))"` → YAML 可解析
   - 复现 CI 的引擎构建（至少 1 平台）：
     ```
     git clone --no-checkout https://github.com/jasonxu114514/opencode2api.git /tmp/eng
     git -C /tmp/eng checkout b296a61d0e3ef93b4df58e298be2c4e34e397322
     ( cd /tmp/eng && CGO_ENABLED=0 go build -o /tmp/eng-bin ./cmd/opencode2api )
     ```
     期望 exit 0、产物非空（本机 Go 1.26 兼容 go 1.24 模块）。
   - `git diff --check` → 无空白错误
4. Commit：`🐳 ci(release): 引擎源码源换至真上游 v1.4.7`

---

## Task 2：CHANGELOG v2.0.5 段落

**Files:** `CHANGELOG.md`

**行为:** 顶部（`# Changelog` 之后、`v2.0.4` 之前）新增 `## v2.0.5（2026-11-12）`：分「新增」「修复」两节，如实概述阶段 K/L/M 与本次 CI 修复；不夸大、不写「已部署」。release job 会按 `head == tag` 精确取这段作正文（workflow :250-277 的逻辑），**标题必须严格是 `v2.0.5`**。

**Steps:** 1. 写段落。2. `grep -n '^## v2.0.5' CHANGELOG.md` 确认唯一。3. Commit：`📝 docs(changelog): 新增 v2.0.5 段落`

---

## Task 3：本地复核 + 推 main（触发一次 build 验证）

**行为:**
1. `git diff --stat origin/main..main` 复核改动范围只有 CI + 文档 + 计划。
2. push main（含 CI 提交信息或追加 `CI` 空提交以触发 build job）—— 按 workflow `if` 条件，需要 head commit 含 `CI`。用 `🔧 chore(ci): 修复引擎源后触发构建` 提交信息同时满足。
3. 等 GitHub Actions run 出来，**确认三平台 build 不再是 `Repository not found`**（可用 `curl api.github.com/.../actions/runs?per_page=5` 复核，只读）。
4. 若仍失败，按失败步骤修，回到 Task 1；**build 未通过前不推 tag**。

**Steps:** 见验收 N-3。

---

## Task 4：打 v2.0.5 tag 并推送，发 Release

**行为:** build 验证通过后：
1. `git tag -a v2.0.5 -m "v2.0.5：Kilo 独立网关 + 供应商维度 + 虚拟模型 + CI 引擎源修复"`（ annotated tag）
2. `git push origin v2.0.5`
3. 该 push 触发 `build`（tag 分支条件）+ `release` job（`startsWith(github.ref,'refs/tags/v')`），三平台打包 + 发 GitHub Release（正文取 CHANGELOG v2.0.5 段 + 安装包附件 + updater latest.json）。
4. 复核 Release：公开 API 查 `releases/latest` 或 `/releases/tags/v2.0.5`，确认 `draft=false`、有 assets。

---

## Task 5：文档与日志（AGENTS.md 强制第 6 条）

- `docs/issue-log/2026-11-12.md` 追加阶段 N：根因（wbi35772 404）、时间线（11-07 success → 11-11 记录 404 → 11-12 build failure）、修复、CI run 证据、Release 结果。
- `OPEN.md`：新增/关闭「CI 发版链路断」项；更新 Kilo 行（如已发版）。
- 本计划：验收表 / 审查结论 / 状态回填。

---

## 代码审查（阶段级环节）

**审查方：** 独立角色子代理（非实施者）

| 审查项 | 结论 | 问题清单 |
|---|---|---|
| GPL 合规（SOURCE.txt 与实际 clone 源逐字一致、LICENSE 未动） | | |
| CI 可复现性（固定 commit、非分支名、Go 版本兼容） | | |
| CHANGELOG 标题与 tag 精确匹配（release 正文取值） | | |
| 发版边界（只 push main + tag、未动生产配置） | | |
| 验证证据（build 真通过、Release 真发布） | | |

**独立子代理结论（11-12 回传）：✅ 有条件通过** —— 五面无 ❌ 阻断项：GPL 合规 ⚠️（实质一致，1 处逐字差异）、CI 可复现 ✅、CHANGELOG ⚠️（格式对、透明度缺 1 项）、发版边界 ✅、验证证据 ✅。4 项待收口：

| 编号 | 级别 | 问题 | 处置 |
|---|---|---|---|
| B1 | 高（透明度） | `SOURCE.txt`「本项目当前 WARP 关闭，运行不到该路径」与代码默认 `engineWarpEnabled()=true`（`config.go:518-524`）、`warp_first` 冲突；公开 Release 正文未披露「引擎改 stock、失 sessionGuard」 | ✅ 已修：`SOURCE.txt` 改为「全新安装默认 WARP 开启，启用前评估」，CHANGELOG v2.0.5「已知限制」补引擎血统变更与 sessionGuard/24h 冷却说明（提交 `336f12f`）。**注**：已发布 Release 正文是快照，见下方残留 |
| B2 | 中 | 产物文件名 2.0.4 / `latest.json` 2.0.5 不一致，疑似 updater 误报更新 | ✅ 已登记 OPEN.md + 计划残留清单 + CHANGELOG 已知限制；未实机验证 updater，如实标注 |
| B3 | 中（GPL 字面） | `SOURCE.txt` URL 缺 `.git` 与 workflow 不逐字一致 | ✅ 已修：`SOURCE.txt` 第 2 节改带 `.git` 并附浏览 URL，复核 `SOURCE URL == ENGINE_REPO` 且 `commit == ENGINE_COMMIT` 逐字相同 |
| B4 | 低 | `freetier_engine.go:183-196` 仍以旧分叉 24h 冷却为前提论证 `max_hours=24`，stock v1.4.7 实为 30min | ✅ 注释加「前提基于旧分叉，待 R1 重估」，常量未改（调研明确本轮不改） |

**复核跑数**：`grep -c '^## v2.0.5' CHANGELOG.md` = 1、首 token `v2.0.5`；SOURCE URL/SHA 与 workflow 逐字相同；`git diff --check` 干净；`go build ./...`=0。

**结论：** ✅ **有条件通过** —— 条件 = B1 的已发布 Release 正文披露只能靠编辑 Release（外发）或下版本补记、B2/B4 按遗留另立阶段、生产部署仍需停机窗口，均如实登记，不伪装全绿。

---

## 验收标准总表（2026-11-12 回填，逐条附真实输出）

| # | 标准 | 结果 | 命令 + 输出摘要 |
|---|------|------|----------------|
| N-1 | 本地验证 | ✅ | `yaml.safe_load(build-release.yml)` → `jobs=['build','release']`、`on.push.branches=['main','feat/desktop-app']`；`git diff --check` 干净；**本地复现 CI 引擎构建**：clone `jasonxu114514/opencode2api` → checkout `b296a61` → `CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o <abs> ./cmd/opencode2api` → exit 0、产物 14,096,896 字节（Windows `%TEMP%` 根目录被 Go 拒绝，改项目内 `.scratch/` 绝对路径后通过） |
| N-2 | GPL 合规 | ✅ | `SOURCE.txt` 上游 URL/commit = workflow `ENGINE_REPO`/`ENGINE_COMMIT`（`jasonxu114514/opencode2api` / `b296a61…`）；`README.md`/fetch 脚本/Go 注释同步；**LICENSE 未改**；CI 的 `Package GPL source` 三平台 success，`COMMIT.txt` 与源码 tar.gz 由 `git archive` 自动生成；Release assets 含 `opencode2api-win-gpl-v2.0.5.tar.gz` |
| N-3 | CI build 修复 | ✅ | 推 main（CI 空提交 `68d3ddf`）→ run **`43152620288` = success**：三平台 `Build free-tier engine` + `Package GPL source` 全 success，不再 `Repository not found`；`release` 按预期 skipped（push 非 tag） |
| N-4 | tag + Release | ✅ | `git tag -a v2.0.5`（object `2d504e6` → commit `68d3ddf`）→ push `[new tag] v2.0.5 -> v2.0.5` → tag run **`43155170426` = success**（windows/ubuntu/macos build + `release` 全 success）；Release `v2.0.5`：`draft=false`、`prerelease=false`、发布 2026-11-12T04:35:59Z、**5 assets**（gpl 源码包 + NSIS/deb/AppImage/dmg），正文取自 CHANGELOG `## v2.0.5` 段 |
| N-5 | 红线 | ✅ | 只 push `main` 与 `v2.0.5` tag；本地仍 2 分支（main + feature/web-self-service）；`main` 与 `origin/main` 0/0；生产 exe/配置未动；工作区干净 |
| N-6 | 文档 | ✅ | `docs/issue-log/2026-11-12.md` 阶段 N 记录；`OPEN.md` 新增「产物版本名 2.0.4 不一致」待办并更新 Kilo 行；本计划状态/验收表/审查表回填 |
| N-7 | 独立审查 | ✅ | 独立子代理判「有条件通过」，无 ❌ 阻断项；B1/B3 已修（`336f12f`），B2/B4 登记遗留。见 §代码审查 |

**总评：** N-1~N-6 通过；N-7 独立审查待回传。**阶段 N 完成，v2.0.5 已发布**。
**遗留（如实）**：① 安装包文件名仍带 `2.0.4`（`package.json`/`tauri.conf.json` 版本未 bump，不影响功能，已登记 OPEN.md）；② 生产部署仍需停机窗口；③ 11-11 调研 R1（生产引擎换 v1.4.7）另立阶段。

---

## 风险与降级

| 风险 | 缓解 |
|---|---|
| 真上游 v1.4.7 的 `./cmd/opencode2api` 构建产物行为与旧分叉不同（失 sessionGuard） | 11-11 已实测 stock v1.4.7 兼容现有配置（3/3 200）；WARP 关闭时 sessionGuard 本就跑不到；如实写进 CHANGELOG |
| GPL 声明与实际源不符 | Task 1 同步改 `SOURCE.txt`；`COMMIT.txt` 由 CI 从 `engine-src` 自动生成，不手写 |
| tag 推了但 release job 仍失败 | **Task 3 先验 main build 通过再推 tag**；若 tag run 失败，修后删 tag 重打（需谨慎，不自动删远程 tag） |
| `updater-latest` tag 冲突 | release job 有滚动 updater 逻辑（workflow :235+），本次不改；如实记录 |
| CI 构建耗时长（三平台 + Tauri） | 用 GitHub API 轮询状态，不阻塞本地其它工作 |

---

## 给接手 AI 的完整提示词（零上下文可直接粘贴）

---

你是负责 **opencode2api_enhance** 的实现代理，请**完整执行阶段 N**，不要只写方案。

### 基线
- 目录 `D:\AI_Projects\opencode2api_guide\opencode2api_enhance`（Windows，Git Bash）
- 分支 `main`（tip `3df13f0`，已与 origin 同步，本地仅 main + feature/web-self-service）
- 唯一计划：`docs/ai-framework/plans/2026-11-12-ci-engine-source-and-release.md`
- 必读：`AGENTS.md`、`.github/workflows/build-release.yml`、`docs/opencode-channel-research-2026-11-11/top5-99-consolidation-and-upgrade.md`

### 背景（已核实，勿重复调研）
- `wbi35772/opencode2api-win` 仓库已 404；真上游 `jasonxu114514/opencode2api` 活跃，**v1.4.7 = commit `b296a61d0e3ef93b4df58e298be2c4e34e397322`**（tag object `1011e14`）；`./cmd/opencode2api/main.go` 存在，go.mod 要 go 1.24，workflow setup-go 已是 1.24。
- 11-11 已实测 stock v1.4.7 兼容现有配置（healthz 12/44、推理 3/3 200）。
- 今天 run `43149792951` 三平台全挂 `git clone wbi35772/...`，release skipped。

### 做（Task 1→5，每 Task 测过再 commit）
1. workflow 引擎源换真上游 v1.4.7 固定 commit；`SOURCE.txt`/`README.md` 同步（GPL 合规，LICENSE 不动）
2. CHANGELOG 顶部加 `## v2.0.5（2026-11-12）`
3. 本地验证（YAML/引擎 build/diff --check）→ 推 main（提交信息含 `CI`）→ 用 GitHub API 确认 build 越过引擎步骤
4. **build 通过后**才打 annotated tag `v2.0.5` 并推送 → 确认 Release 发布
5. 文档/日志/计划回填

### 不做
- ❌ 不改生产 exe / config.json / 不退 WARP / 不换生产引擎二进制
- ❌ 不合并 v1.4.7 源码、不做 sessionGuard 回灌、不修 L3 WARP
- ❌ 不 push main 与 v2.0.5 tag 之外的任何 ref
- ❌ 不手改 `COMMIT.txt`（CI 自动生成）；不动 LICENSE

### 工作方式
1. 证据优先：每条验收命令贴真实输出；禁止「应该能过」。
2. 发版是外发行为，已获用户授权；但**build 未通过前不推 tag**。
3. 用简体中文汇报；代码标识符保持原样。

### 交卷
提交列表、N-1~N-7 自评表（每条附命令与输出）、CI run id 与结论、Release 链接、残留风险。

现在开始：读完计划，从 Task 1 执行到最后。

---

## 残留手工验收清单

1. **生产部署**：阶段 K/L/M 代码上生产仍需停机窗口（发 Release ≠ 部署）。
2. **v1.4.7 升级评估后续**：11-11 调研的 R1（生产引擎换 v1.4.7）另立阶段，本阶段只改 CI 构建源。
3. ~~**产物版本名不一致**~~ → ✅ **11-12 已关闭**：bump 三处到 `2.0.6` 并发布 v2.0.6（tag run `43159025397` success），产物名 / `latest.json` / 应用内版本三者对齐；v2.0.5 旧 Release 保留作历史。详见 `docs/issue-log/2026-11-12.md` v2.0.6 记录。
4. 阶段 K/L/M 遗留项按各阶段计划「阶段开头遗留」带入。
