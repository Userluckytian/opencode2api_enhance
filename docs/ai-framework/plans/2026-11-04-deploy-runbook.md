# 插件整改部署 Runbook（2026-11-04）

> 配套计划：[2026-11-04-plugin-remediation.md](2026-11-04-plugin-remediation.md)
> 原则：**先副本、后金丝雀、再停机窗口**；不覆盖运行中 exe；RunBios 不动。
> 本文只写操作步骤，不代表已上线。

---

## 1. 当前状态

| Phase | 内容 | 状态 |
|---|---|---|
| P1 | MiniMax 1M 别名 + LobsterAI `context_window` | 副本通过 |
| P2 | MiniMax `data/accounts.json`、LobsterHub 默认 `data/`、迁移脚本 | 代码/脚本通过，**待迁移** |
| P3 | QwenWork 真实模型 + 对话错误帧 + MiniMax 持久化 | 副本通过 |
| P4-A/B/C/D | Grok 删除清文件 / 额度 / 轮询友好 / 夜间窗口 | 副本通过 |
| P4-E | 事件日志 Grok + QwenWork | 副本通过（其余插件未接） |
| P5 | 9 个有 UI 插件视觉/动效；xhx N/A | 副本通过 |

**全量构建结果：** mmxproxy、lobsterai、lobsterhub、qw2api、grok、buddyhub、skywork、loomy、flonno 均 `go build ./...` = 0。

---

## 2. 副本位置（最终候选）

| 插件 | 副本路径 |
|---|---|
| mmxproxy | `D:\AI_Projects\_plugin-work\phase3\mmxproxy` |
| qw2api | `D:\AI_Projects\_plugin-work\phase3\qw2api` |
| lobsterai | `D:\AI_Projects\_plugin-work\phase5\lobsterai` |
| lobsterhub | `D:\AI_Projects\_plugin-work\phase5\lobsterhub` |
| grok | `D:\AI_Projects\_plugin-work\phase4\grok` |
| buddyhub | `D:\AI_Projects\_plugin-work\phase5\buddyhub` |
| skywork | `D:\AI_Projects\_plugin-work\phase5\skywork` |
| loomy | `D:\AI_Projects\_plugin-work\phase5\loomy` |
| flonno | `D:\AI_Projects\_plugin-work\phase5\flonno` |

> 同一插件的早期副本（phase1/phase2）只作对照，不要用来替换。

---

## 3. 上线前检查（每插件）

```powershell
# 1) 构建
cd <副本>
go build ./...
go vet ./...
go test ./...          # 若有测试；LobsterAI 的 E2E 基线失败见日志

# 2) 备份
$id = '<插件 id>'
$dir = "D:\Program Files\opencode2api\bin\providers\$id"
Copy-Item "$dir\provider.json" "$dir\provider.json.bak-$(Get-Date -Format yyyyMMdd-HHmm)"
Copy-Item "$dir\$id.exe" "$dir\$id.exe.bak-$(Get-Date -Format yyyyMMdd-HHmm)"
# data/ 也整目录备份
```

**必须停用插件**（宿主面板或 `Stop-Process` 对应进程）后再替换 exe；Windows 不允许覆盖运行中的文件。

---

## 4. P2 数据迁移（唯一需要停机窗口的步骤）

脚本：`scripts/migrate-plugin-data.ps1`

```powershell
# 1) 先 DryRun，核对计划
powershell -NoProfile -File scripts\migrate-plugin-data.ps1

# 2) 确认输出 4 条计划、目标路径正确后，停用 LobsterAI / LobsterHub / MiniMax
# 3) 真执行（自动备份 *.bak-<时间>）
powershell -NoProfile -File scripts\migrate-plugin-data.ps1 -Execute
```

**脚本做什么：**

- LobsterAI：`provider.json` 的绝对 `auth_dir` → `<插件>/auths`，并改为相对 `"auths"`；
- LobsterHub：`%APPDATA%\LobsterHub` → `<插件>/data`（移动，与桌面版分家）；
- MiniMax：只报告根 `accounts.json` → `data/accounts.json`（插件启动自动复制）；
- Flonno/RunBios：明确不迁移 1GB profile。

**注意：** LobsterAI refresh token 一次性，**只能移动不能复制**；脚本已按移动处理。

---

## 5. 逐插件替换顺序（建议）

1. **MiniMax**（低风险：数据自动迁移 + UI）
2. **LobsterAI**（凭证迁移，需停）
3. **LobsterHub**（数据迁移，需停）
4. **QwenWork**（真实模型 + 事件日志 + UI）
5. **Grok**（删除清文件 + 额度/轮询/夜间；事件日志）
6. **Skywork / BuddyHub / LOOMy / Flonno**（UI/动效）

每个插件替换后：

- 看宿主面板是否 ready；
- 打开面板 5 个页面；
- 发 1 次对话；
- Grok 额外测"删号 → 刷新 → 不复活"；
- MiniMax 额外测"同步 → 切页/刷新 → 积分仍在"。

失败：停插件，恢复 `*.exe.bak-*` / `provider.json.bak-*` / `data` 备份，再启动。

---

## 6. 金丝雀（不替换线上）

```powershell
# 复制插件为 <id>-dev，改 provider.json 的 id/panel_port，宿主会新拉一个实例
# 只用于面板/对话验收；账号数据用副本，避免与线上抢号
```

金丝雀通过后再走第 5 节替换。

---

## 7. 已知缺口（不要当成已完成）

- **P4-E 事件日志**：仅 Grok + QwenWork；BuddyHub/MiniMax/LobsterAI/Hub 未接。
- **P2 profile**：Flonno/RunBios 临时 profile 未实测；1GB 共享 profile 未迁移。
- **Grok `register_clash_*`**：明确未接线。
- **严格 RR**：未改 grok2api selector；当前是"轮询友好"预设。
- **真实模型**：QwenWork 需一次真实请求核对 `real_model`。
- **UI**：只做 token/动效，未做截图级回归。
- **LobsterAI E2E**：`TestSuspendedUpstreamSurfacesInStatus` 基线失败，非本次引入。

---

## 8. 回滚

| 对象 | 回滚 |
|---|---|
| exe | `Copy-Item <id>.exe.bak-<ts> <id>.exe` |
| provider.json | 恢复 `.bak-<ts>` |
| MiniMax 号池 | `data/accounts.json` 保留；根 `accounts.json` 未被删 |
| LobsterAI 凭证 | 从 `.bak-<ts>` 移回原 auth_dir（需停插件） |
| LobsterHub 数据 | 从 `%APPDATA%\LobsterHub.bak-<ts>` 移回 |
| 事件日志 | `data/events.jsonl` 可直接删除，不影响账号 |
