# MASTER 目标 — 插件进程孤儿治理

> 目标模式（§12.2）的全局地图。每个模块的完成判据**不因实施中细化而变**。
> 建立日期：2026-11-10

---

## 一、背景与问题陈述（2026-11-10 实测）

当前架构为三层进程：

```
opencode2api.exe（管理面 :40000）
  └─ opencode2api.exe -provider-service（:50080）   ← 插件之父
       └─ <plugin>.exe --provider-serve            ← 插件
            └─ mihomo / chrome / sing-box          ← 孙进程
```

**实测发现三个缺陷**（非推测，均有代码位置或现场证据）：

| # | 缺陷 | 证据 |
|---|---|---|
| D1 | 设置页「残留进程清理」按钮点了无反应（后端 404） | 前端 `SettingsPage.tsx:292-315` + `api.ts:1291`；后端路由在 `ded4d0e` 被整体删除 |
| D2 | 停插件不杀进程树，孙进程变孤儿 | `pluginprovider/proc_windows.go:22` 为 `taskkill /PID <pid> /F`，无 `/T` |
| D3 | provider-service 无父进程自检，管理面死亡后仍存活并自我重启插件 | `providerservice.go` 全文无父进程检测；`Watchdog` 会持续拉起 |

**现场证据（2026-11-10 09:00 清理前）**：`_plt4-browser\providers\` 下 9 个插件进程 + Temp 下 2 个 + rb.exe，共 13 个进程父进程已死但仍在运行；已由人工清理完毕（0 失败）。

**已验证为「非缺陷」的项**（避免重做）：
- `reapOrphans` 正常工作中（日志 11-08 回收 7 个、11-09 回收 11 个）
- `skywork` 状态文件 disabled 且进程确实未运行，状态一致
- mihomo 残留属 ARTEX 项目（`D:\AI_Projects\VPN-all\kiteVPN`），与本项目无关

---

## 二、模块清单与完成判据

| 模块 | 内容 | 完成判据（不可变更） |
|---|---|---|
| **M1 可见性** | 恢复设置页孤儿扫描/清理能力，判据按当前架构重写 | ① `GET /api/admin/processes/orphans` 返回 200 且结构匹配前端 `OrphanScan` 类型；② 能扫出「外来 providers 目录的已死父进程插件」；③ `POST .../kill` 能清理且只允许杀扫描结果内 PID；④ 有单测覆盖分类纯函数；⑤ 前端零改动可用 |
| **M2 止血** | 消除新孤儿产生的根因 | ① `taskkill` 带 `/T`；② provider-service 检测父进程消失后自退；③ 单元/集成测试证明树杀生效 |
| **M3 自动回收** | `reapOrphans` 覆盖孙进程 | ① 孙进程（mihomo/chrome/sing-box）在父插件死后被回收；② 不误伤其它项目的同名进程 |

---

## 三、全局红线（目标模式下仍需授权）

1. **不 push 到远程**（AGENTS.md 强制第 4 条）
2. **不改动生产环境** `D:\Program Files\opencode2api\`（部署需单独停机窗口 + 用户授权）
3. **不引入外部依赖**（项目当前零外部依赖，Job Object 需 `golang.org/x/sys` → 需单独决策）
4. **不杀非本项目进程**：扫描/清理逻辑必须限定在 `--provider-serve` 特征 + 父进程已死双重条件
5. **密钥不进 git**

---

## 四、模块依赖顺序

```
M1（可见性，可独立交付）
  ↓
M2（止血，宿主侧改动，影响全部 14 插件）
  ↓
M3（自动回收，依赖 M2 的树杀语义）
```

M1 不依赖 M2/M3，可先独立上线。M3 必须在 M2 之后，否则树杀尚未生效时扩展回收范围意义有限。

---

## 五、明确的非目标

- ❌ 不做 Job Object（P3 级别，需 `x/sys` 决策，另开工单）
- ❌ 不做 siftq 插件化改造（独立议题）
- ❌ 不恢复多实例/探针（那套架构已随 `ded4d0e` 移除，不复活死代码）

---

## 六、终止条件

M1/M2/M3 的完成判据全部达成，且无「不通过」项 → 目标完成。
「待人工」项（如真机部署验证）登记后不阻塞，终态为「有条件完成」。

---

## 七、进度看板（滚动更新）

| 模块 | 状态 | 判定/证据 | 更新时间 |
|---|---|---|---|
| M1 | ✅ 完成（P0=0） | A1/A2/A3/A5/A6 全绿；A4/A8 已于 T3 隔离实例补证（200 + 正向孤儿列出→kill） | 2026-11-10 |
| M2 | ✅ 完成（P0=0，B1-B8 全绿） | 树杀 `/T` + provider-service 父进程自检；B4 全清 4697ms、B5 孙进程 946ms 消失；审查 P1（macOS procfs）/P1b（Linux ErrProcessDone）/P1-1（POSIX 捕获+reparent）/P1-2（CIM 故障误退）全部修复；主会话终审通过（M2 计划 §九） | 2026-11-10 |
| M3 | ✅ 完成（路线 A，P0=0，C1-C8 全绿） | `internal/proctree.FindDescendants`（深度≤2/环防护）+ `killOrphanTree`（Windows `/T`、非 Windows 显式后代枚举）+ 端点独立 `killProcessTree`；`reapOrphans` 改为真实父进程探活（`pidAliveConservative`）；隔离实测自动回收 1926ms、手动端点双 PID 消失、ARTEX 5 mihomo + 7 verge-mihomo `NO_DIFF`；独立审查 3 个 P1 已修并复核；主会话终审通过（M3 计划 §八） | 2026-11-10 |

**M1 契约补证状态**：✅ 全部完成（GET 200/结构匹配/正向孤儿/kill 越界拒绝，见 M1 计划 §十一）。
**M2 关键决策记录**：grok supervisor = 用户开机自启（external 模式），树杀仅限插件自身后代，不写特例；`core/manager/process_windows.go` 的 killProcess 维持不加 `/T`（其孙进程问题由 M3 兜底）。

---

## 八、目标终态（2026-11-10）

**判定：有条件完成**。M1/M2/M3 完成判据全部达成，无「不通过」项；剩余项均为显式登记的已知限制与待人工项：

| 项 | 内容 | 归属 |
|---|---|---|
| 「父先崩」无主孙进程 | 插件自身先退出的场景 PPID 关联丢失，路线 A 不承诺；根治需路线 B（运行期带创建时间快照） | 后续阶段（需单独授权） |
| PID 复用窗口 | 枚举→探活→杀之间未校验创建时间；经审查批准显式接受 | 路线 B 一并解决 |
| POSIX reparent | 非 Windows 自动回收正向场景不承诺（PPID→init）；WSL 仅负向+纯逻辑 | 平台限制 |
| 非 Windows 正常停插件 | Toggle/Close 仍单 SIGKILL 不树杀；孤儿回收与端点已覆盖 | 平台限制（生产 Windows） |
| 生产部署 | `D:\Program Files\opencode2api\` 需停机窗口 + 用户授权 | 待人工 |
