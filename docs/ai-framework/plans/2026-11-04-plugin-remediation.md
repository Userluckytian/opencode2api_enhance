# 插件整改阶段计划（2026-11-04）

> 状态：P1 副本验证通过；P2 代码/迁移脚本完成（待上线）；P3~P5 待实施
> 依据：`docs/plugin-audit-2026-11-04.md`、`docs/PLUGIN-CAPABILITIES.md`
> 实施：`qw2api/qwork-auto`；审查：`mmxproxy/MiniMax-M3.1-Flash`；验收：主代理（命令/证据）
> 纪律：只改副本/worktree；线上替换等停机窗口；RunBios 不动。

---

## 1. 目标

1. 数据合规：账号/额度/日志/事件落各自插件 `data/`。
2. 显示正确：`context_window` / `max_output_tokens` 真实或明确标"上游未提供"。
3. 关键功能：QwenWork 真实模型+对话；MiniMax 持久化；Grok 删除复活/额度/轮询。
4. 事件日志：注册/签到/切号/额度可查。
5. UI 统一：白底+抹茶/奶酪（无粉），RunBios 基准；Skywork 纳入；MiniMax 彻底换。
6. 可回滚：每插件独立上线/回滚。

**非目标**：不改宿主契约；不做公共 autoreg 包；不做严格 RR（需改 grok2api）；RunBios 不改。

---

## 2. 已确认决策

| # | 事项 | 决策 |
|---|---|---|
| 1 | LobsterHub 数据 | 合规 `<插件>/data/`；`%APPDATA%` 一次性迁移后与桌面版分家 |
| 2 | Flonno/RunBios profile | 优先临时 profile；不迁移 1GB；变差则保持现状；禁止复制 |
| 3 | Skywork/MiniMax | Skywork 纳入 UI；MiniMax 彻底换 |
| 4 | Grok 轮询 | A：插件侧"轮询友好"+可观测；严格 RR 另立项 |
| 5 | 动效 | A：QwenWork 先做 5 个低风险效果 |
| 6 | 实施子代理 | qw2api 实施 + MiniMax 审查 + 主代理验收 |
| 7 | MiniMax 1M | 支持可选档；`-1M` 别名，默认 512K |
| 8 | 上下文/输出 | LobsterAI 加键；Grok 需改 grok2api；LobsterHub 先验证；无 output 的标"上游未提供" |

---

## 3. 通用工作流

```
副本/worktree → 实施(qw2api) → 审查(MiniMax) → 构建/测试(主代理)
→ 金丝雀 <id>-dev → 验收 → 停机窗口上线 → 回滚(.bak)
```

硬规则：不在 `Program Files\...\providers\` 改源码/覆盖运行中 exe；每 Phase 独立目录；只改白名单文件；每 Phase 更新 issue-log。

---

## 4. Phase 总表

| Phase | 内容 | 人日 | 线上影响 | 状态 |
|---|---|---:|---|---|
| P0 | 基线/副本/构建/验收脚本 | 0.5 | 无 | 部分完成 |
| P1 | MiniMax 1M 别名 + LobsterAI context_window | 2 | 无（副本） | ✅ 副本通过 |
| P2 | 数据合规：LobsterAI/LobsterHub/MiniMax/profile | 5~6 | 停机 1~5min/插件 | 代码/脚本完成，待迁移 |
| P3 | QwenWork 真实模型/对话 + MiniMax 持久化 | 4 | 替换 exe | 待实施 |
| P4 | Grok 删除/额度/轮询 + 事件日志 | 9 | 替换 exe + grok2api | 待实施 |
| P5 | UI 统一 + 动效 | 15 | 逐插件替换 | 待实施 |

总计约 32~36 人日（不含 P6 可选）。

---

## 5. P1（已完成）

**MiniMax**：`handlers.go` 读 `context_window_options`；基础 512K；新增 `MiniMax-M3-1M` / `MiniMax-M3.1-Flash-1M`（1M）；`proxy.go` 剥 `-1M` + 传 `context_limit:1000000`，非 1M 不传。
**LobsterAI**：`models()` 双写 `context_window`。

副本：`D:\AI_Projects\_plugin-work\phase1\{mmxproxy,lobsterai2api}`
验证：mmx `build/vet/test` 通过；lobster `build/vet` 通过；`TestSuspendedUpstreamSurfacesInStatus` 为基线失败；原始目录未动。
审查处理：删死代码、去跨 provider 去重、`-1M` 白名单、hints 透出、补 3 个单测。
遗留：`context_window_options` 强类型对字符串数字不容错；空号池 fallback 无 caps。

---

## 6. P2（代码/脚本完成，待迁移）

### P2-A LobsterAI
- 默认 `<插件>/auths` 已合规；问题是安装态 `provider.json` 写了绝对路径。
- 迁移脚本：`scripts/migrate-plugin-data.ps1` 移动凭证 + 改 `auth_dir="auths"`；要求停插件。

### P2-B LobsterHub
- 代码：`pluginDataDir(configured, dir)`：未配置时 `<插件>/data`；桌面版默认不变。
- 测试：`plugin_data_dir_test.go`。
- 迁移：脚本把 `%APPDATA%\LobsterHub` 文件移到 `<插件>/data`；与桌面版分家。
- 副本：`D:\AI_Projects\_plugin-work\phase2\lobsterhub`；build/vet/test 通过。
- 注：拷贝时漏了 `browserid/`、`lockfile/`、`build/`，已补；后续建副本不要漏。

### P2-C MiniMax
- 代码：`migrateAccountsToData(dir)`：`data/accounts.json`，旧根文件存在且新文件不存在时复制（保留旧文件）；`provider_serve.go` 使用；独立版不变。
- 测试：`store_migrate_test.go`（旧→新、新文件不覆盖、幂等、日志一行、无 tmp 残留）。
- 副本：`D:\AI_Projects\_plugin-work\phase2\mmxproxy`；build/vet/test 通过。

### P2-D Profile
- Flonno/RunBios：本轮不迁移 1GB；临时 profile 方案待实测通过率；失败保持现状并登记例外。
- **不要**复制 `~/.qw2api/chrome-autoreg`。

### P2 上线检查
- 停插件 → 备份 → 脚本 DryRun 核对 → `-Execute` → 启动 → 面板核对账号/额度。
- 一次只动一个插件；LobsterAI refresh token 只能移动。

---

## 7. P3：QwenWork + MiniMax 持久化

### P3-A QwenWork 真实模型
- `upstream` 解析 `raw_usage.model`；`RequestRecord.RealModel`；`chat.go` 写入；面板日志列/调试 meta/命中卡。
- 验收：`qwork-auto` 一次请求，与 creditprobe 一致。

### P3-B QwenWork 对话
- `debugRelayStream` 错误帧；`relaySSE` 解析 error/usage/finish；命中卡按 request_id；多轮可选。
- 验收：402/429 可见原因；并发不串。

### P3-C MiniMax 持久化
- `Account.Balance` 或 `data/balance-cache.json`；启动回填；同步完成 `loadPool()`。
- 验收：同步 104 号 → 切页/刷新/重启仍在。

---

## 8. P4：Grok + 事件日志

- P4-A 删除复活：清文件（预览/匹配行）、注册后增量导入、孤儿清理、`deletion-preview`+`linkedDeleteTargets`、`auto_import` 警示。
- P4-B 额度：补 `used/usagePercent/unit/periodEnd/nextProbeAt/source/confidence`；进度条/重置/详情/刷新全部。
- P4-C 轮询：暴露 priority/maxConcurrent/minRemaining；"轮询友好"预设；命中账号/换号原因/分布。
- P4-D 夜间/Clash：`pool_window_*` 生效；`register_clash_*` 接线或明确未实现。
- P4-E 事件日志：`data/events.jsonl` + API + 面板；首批 Grok/QwenWork/BuddyHub/MiniMax/LobsterAI/Hub。

---

## 9. P5：UI + 动效

- P5-A token（5）：QwenWork/Skywork/Grok/BuddyHub/LobsterAI/LobsterHub/LOOMy。
- P5-B 布局/暗色（7）：xhx、MiniMax、Flonno shell、侧栏/页头/卡片/表格。
- P5-C 动效（3）：count-up/额度条/状态 pulse/toast/按钮；`prefers-reduced-motion`。
- 设计令牌见报告 4.2；动效见 4.5。

---

## 10. 子代理编排

| 角色 | 模型 | 权限 |
|---|---|---|
| 实施 | `qw2api/qwork-auto` | 只改白名单；不 push/不删目录 |
| 审查 | `mmxproxy/MiniMax-M3.1-Flash` | 只读 |
| 验收 | 主代理 | 构建/测试/截图/回滚 |

```js
phase("implement");
const impl = await agent(handoffPrompt, { provider: "local-proxy", model: "qw2api/qwork-auto" });
phase("review");
const review = await agent(reviewPrompt + "\n---\n" + impl, {
  provider: "local-proxy", model: "mmxproxy/MiniMax-M3.1-Flash"
});
return { impl, review };
```

失败：构建错误回给实施者 ≤2 次；审查阻断由主代理裁决；402/429 重试+回退默认模型。

---

## 11. 上线检查表

- [ ] 副本 build/vet/test
- [ ] canary `<id>-dev` 5 页 + 1 对话
- [ ] 备份 exe/provider.json/data
- [ ] 停插件 → 确认进程退出
- [ ] 替换 exe（留 `.bak`）→ 启动 → ready
- [ ] 面板 5 页 + 1 对话 + 数据核对
- [ ] 观察 10 分钟；失败还原 `.bak`

---

## 12. 风险

| 风险 | 缓解 |
|---|---|
| 覆盖运行中 exe | 停→备份→替换→启；canary |
| refresh token 复制 | 整目录移动；禁止复制 |
| 迁移丢账号 | 迁移前整目录备份；失败回滚 |
| UI 回归 | 只改样式；5 页截图 |
| MiniMax 1M 计费 | 独立 `-1M`；默认 512K |
| Grok 删除复活 | 文件+DB+关联+增量导入 |
| qw2api 池不稳定 | 重试/回退；任务拆小 |

---

## 13. 当前下一步

1. P2：用户停机窗口执行迁移脚本（LobsterAI/LobsterHub），MiniMax 启动自动迁移。
2. P3：QwenWork 真实模型/对话 + MiniMax 持久化（副本 → 验收）。
3. P4/P5 依次。
