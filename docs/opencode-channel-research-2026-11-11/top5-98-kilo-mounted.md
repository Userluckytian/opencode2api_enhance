# Kilo Free 作为免费供应商挂载（2026-11-11 16:20）

> 需求：把 Kilo 像 OpenCode 一样挂在底层当一个免费供应商，并在页面上标明「数据可能被用于训练」。
> 状态：**已挂载 + 端到端实测通过 + 警示今晚即可见**；结构化字段改动已提交到代码，随下次面板/宿主上线。

---

## 1. 挂载形态（决策依据）

Kilo 挂在 **`providers[]` 里的 `type:"custom"` 条目**（供应商服务 50080），不是挂在 40080 免费引擎里。理由：

- `custom` 源天生支持「无 key」（测试端点 `custom_providers.go:658` 就有 `keys = []string{""}` 无 key 网关分支），配置即可，**零生产代码改动**；
- 与 OpenCode 免费层天然解耦：40080 挂了不影响 Kilo，反之亦然；
- 走已有厂商级 failover（`core/router/router.go` 的 `Candidates()`），不需要为它新增调度分支。

**配置**（写入 `%APPDATA%\opencode2api-manager\config.json` 的 `providers[]`）：

```jsonc
{
  "id": "kilo",
  "name": "Kilo Free ⚠️提示词可能被上游用于训练",
  "type": "custom",
  "enabled": true,
  "params": {
    "base_url": "https://api.kilo.ai/api/gateway/v1",
    "protocol": "openai",
    "api_keys": ["anonymous"],        // 上游匿名档；实测不发 Authorization 也通
    "key_strategy": "round_robin",
    "key_403_cooldown": 600,
    "proxy": "",                       // 不勾 use_proxy：这条 lane 不依赖 7890
    "use_proxy": false,
    "allowed_models": [ /* 11 个免费档，见下 */ ],
    "data_policy_note": "提示词可能被上游用于训练"
  }
}
```

白名单只放 11 个 `isFree` 档（上游共 386 个模型，其余是付费档，全暴露会诱导误用）：
`kilo-auto/free`、`nvidia/nemotron-3.5-lightning:free`、`nvidia/nemotron-3.5-super-vl:free`、
`nvidia/nemotron-3-ultra-550b-a55b:free`、`nvidia/nemotron-3-nano-omni-30b-a3b-reasoning:free`、
`cohere/north-mini-code:free`、`thinkingmachines/inkling-2-small:free`、`liquid/lfm-3-1.7b:free`、
`dots-studio/dots-3-note-preview:free`、`stealth/glyph-cluster`、`openrouter/free`

## 2. 实测证据

| 项 | 结果 |
|---|---|
| 挂载方式 | 一次性 Go 工具（`%TEMP%\oc-refs-20261111\mountkilo\`）：GET 现表 → 追加 → 整表 POST `/api/admin/custom-providers/save`。**用 Go 而不是 PowerShell 是因为 PS5.1 会把中文源名解码成乱码，整表回写会污染配置** |
| 幂等/防洗表 | 保存前 dry-run 列出 19 个现有源的 `keys/allowed/enabled`；保存后 **19 → 20，逐源指纹一致** |
| 落盘核对 | `kilo name bytes=52 runes="Kilo Free ⚠️提示词可能被上游用于训练"`；`nv="英伟达"`、`yn="越南-1亿-走代理"`、`hor="octomind-20刀--耗尽"` **中文完好** |
| 探测 | `POST /custom-providers/probe {id:kilo}` → `ok:true 632ms` |
| 目录 | `models_all=386`、`allowed=11`、`models=11`、`keys_available=1` |
| 50080 列表 | `kilo/kilo-auto/free` 等 11 个已出现，`owned_by:"kilo"`（客户端可据此识别） |
| 端到端推理 | `kilo/nvidia/nemotron-3.5-lightning:free` → **200 3379ms**，`content=[KILO-MOUNTED]`；`kilo/kilo-auto/free` → **200 5496ms** |
| 出口独立性 | Kilo **关掉代理仍 200**（而通用 https 直连在本机 `无法连接到远程服务器`，egress 7890 = 64.81.37.124）⇒ 这条 lane 与 OpenCode 的海外出口依赖不同源，是真独立的兜底 |

## 3. ⚠️ 静默换模型（比原报告更常见，实测两次都命中）

| 请求 | 上游实际返回的 `model` |
|---|---|
| `kilo/kilo-auto/free` | `nvidia/nemotron-3-ultra-550b-a55b:free` |
| `openrouter/free`（早前单测） | `recursive/flint-v1.0:free` |

⇒ 「指定模型名 ≠ 拿到的模型」。面板/日志里必须看响应的 `model` 字段，不能信请求名。**已知缺口**（见 §6）。

## 4. 训练警示：今晚靠 name，长期靠字段

- **今晚生效（无需重启宿主）**：警示写在 `name` 里，而面板 `CustomModelsPage.tsx:988` 渲染 `{p.name || p.id}` —— 已核对**部署中**的产物（`D:\Program Files\opencode2api\bin\dist`，构建于 11-08，源码 commit `c90868f`）就是这一行 ⇒ 用户一进「自定义模型源」页就看到 `Kilo Free ⚠️提示词可能被上游用于训练`。
- **长期（已改代码，下次面板/宿主上线生效）**：新增结构化字段 `params.data_policy_note` + 面板玫瑰色徽标（`title` 挂全文），不再依赖把它塞进名字。

数据去向的依据（不是猜测）：`/api/gateway/models` 返回里 **11/11 个免费模型 `mayTrainOnYourPrompts = true`**；NVIDIA 免费端点 ToS 原文写在 model description 里：
> "Trial use only - do not submit personal or confidential data. Your use is logged for security purposes and to improve NVIDIA products and services."

## 5. 代码改动（gofmt/vet/build/test/tsc 全绿）

| 文件 | 改动 |
|---|---|
| `vendors/custom/registry.go` | 新增 `ParamDataPolicyNote = "data_policy_note"` 常量 + 为何做成配置数据而非写死 id 的注释 |
| `custom_providers.go` | ① `customProviderView.DataPolicyNote`（出参）② `customProviderInput.DataPolicyNote *string`（入参，指针=可分三态）③ `dataPolicyNoteFromParams()` 读盘助手 ④ `applyCustomProvidersSave` 落盘三态：**缺键→保留旧值 / 非空→写入 / 空串→清除**（与 `allowed_models` 同款语义，防整表提交洗掉提示——即问题 4 的那类数据丢失） |
| `src/lib/api.ts` | 两个类型各加 `data_policy_note` |
| `src/pages/CustomModelsPage.tsx` | 卡片头部玫瑰色徽标（`title` 提示全文）；`toInputs` 原样回传该字段 |
| `vendors/custom/custom.go` | 中途加过 `Config.DataPolicyNote` 后又**撤掉**：视图直接读 `pc.Params`，那个字段会是死代码（YAGNI） |

`gofmt -w custom_providers.go` 只影响本文件（HEAD 版本就是 clean 的，只有我插入的两行需要重排）；`registry.go` 的 `gofmt -l` 报告是 **HEAD 既有**（已用 `git show HEAD:… \| gofmt -l` 证实），未顺手重排以免扩大 diff；`git diff --stat` 两文件合计 **35 insertions / 3 deletions**，非整文件重写。

## 6. 已知缺口（如实登记，未做）

1. **`auto` 虚拟模型可能把 Kilo 选进去**：`autoCandidateModels()` 走 `visibleFreeModels(globalAgg)`（`auto_model.go:266-297`），`custom.Vendor.ListModels` 对**所有** custom 源硬编码 `Free: true`（`custom.go:332-333`）⇒ Kilo 的 11 个模型在「免费模型」集合里。**当前安全**：`auto_model.enabled=false` 且白名单为空；但**将来若开启 auto，会把可能含私有代码的编码流量随机打到会训练的上游**。修法：白名单排除 `kilo/` 前缀，或让徽标字段参与候选过滤。
2. **免费模型视图不显示来源**：`OpenCodePage.tsx` 的模型列表没有 provider 归属 ⇒ 用户在「挑模型」那一步看不到警示（在「源列表」页能看到）。要做完整需给 `contract.Model` 加 meta 并在目录面透出。
3. **面板/宿主都没重建**：生产 exe（11-06 构建）与 `dist`（11-08 构建）都**不认识** `data_policy_note` 键 ⇒ 徽标暂时不显示（警示靠 name 已生效）。
   ~~早前记的「宿主 API 返回乱码源名」是**我的工具问题**，不是宿主缺陷~~ —— 已用 Go 客户端复核：`/api/admin/custom-providers` 返回 `nv="英伟达"`、`yn="越南-1亿-走代理"`、`kilo="Kilo Free ⚠️提示词可能被上游用于训练"` **全部正确**；之前的乱码来自 PowerShell 5.1 把无 BOM 的 UTF-8 按 ANSI 读。
4. **宿主 exe 被运行中进程锁定**（本仓库 `OPEN.md` 已多次登记），要部署后端改动需停机窗口。
5. **`bin/` 里遗留 v1.4.6 备份** `opencode2api-win.exe.bak-v146-20261111-154229`（13.7MB，回退用；确认稳定后可删）。
