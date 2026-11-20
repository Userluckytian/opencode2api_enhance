# 贡献指南

> ⚠️ 本项目**仅供学习参考，非授权禁止商用**（详见 [LICENSE](LICENSE)）。允许非商业目的的学习、修改与传播分享。
> 提交贡献（代码、文档、建议）即视为同意本项目许可条款。

## 本地检查

提交前请运行：

```bash
make fmt
make test
make vet
make build
```

如果改动 release 逻辑，再运行：

```bash
make release-snapshot VERSION=v0.0.0-dev
```

## 开发原则

- 优先保持现有 API 行为不变。
- 对转换逻辑、配置逻辑和 release 脚本增加小而明确的测试或验证步骤。
- 不要提交 `config.json`、`stats.json`、日志或 release 产物。
- 文档和代码行为一起更新。

## 提交信息

遵循 [AGENTS.md](AGENTS.md) 的提交规范：`<gitmoji><type>(<scope>): <中文描述>`。

| type | 说明 | gitmoji |
|------|------|---------|
| feat | 新功能 | ✨ |
| fix | 修复 bug | 🐛 |
| docs | 文档更新 | 📝 |
| style | 代码格式（不影响功能） | 🎨 |
| refactor | 重构 | ♻️ |
| test | 测试 | ✅ |
| chore | 构建/工具 | 🔧 |
| perf | 性能 | ⚡ |
| ci | CI/CD | 🐳 |
| revert | 回滚 | ⏪ |

- 描述使用**中文**，祈使语气，结尾不加句号
- 首行尽量不超过 50 字符

## 推送时弹出「Select an account」

**现象**：`git push`（或 IDE 的提交/同步）每次都弹 GCM 窗口让你选账号：

```
Select an account
GitHub
Userluckytian
x-access-token
Continue / Add a new account
```

**原因**：Windows 凭据管理器里同一个 host 存了**多个** GitHub 条目
（`LegacyGeneric:target=git:https://github.com` 与 `git:https://x-access-token@github.com`），
Git Credential Manager（`credential.helper=manager`）无法自动判定用哪个，于是每次询问。

**修法**（指定账号，一次生效）：

```bash
git config --global credential.https://github.com.username Userluckytian
```

撤销：`git config --global --unset credential.https://github.com.username`

**验证已修好**（应直接返回 username/password 且不弹窗）：

```bash
printf "protocol=https\nhost=github.com\n\n" | GIT_TERMINAL_PROMPT=0 GCM_INTERACTIVE=never git credential fill
```

**排查备注（都是踩过的坑）**：

- Git Bash 里 `cmdkey /list` 会被 MSYS 路径转换破坏（`/list` → `C:/Program Files/Git/list`），
  必须写 `cmdkey //list`；输出是 GBK，可接 `| iconv -f GBK -t UTF-8`。
  用坏掉的命令会得出「凭据不存在」的错误结论。
- 不要用 `credential.https://github.com.helper=wincred` 去「绕开」弹窗：实测 wincred
  **读不到** GCM 存的凭据（`git credential fill` 返回空），会把认证彻底弄断。

## 发布（打 tag 触发 CI）

1. 版本号四处保持一致：`src-tauri/Cargo.toml`、`src-tauri/tauri.conf.json`、
   `src-tauri/Cargo.lock`、`package.json`。
   > 只改 Cargo.toml 不改 Cargo.lock 不会让 CI 失败（工作流没用 `--locked`），
   > 但构建时锁文件会被静默改写，产物溯源变脏。
2. `CHANGELOG.md` 的段落标题必须写成 `## vX.Y.Z（日期）`——Release 正文从这个标题精确匹配提取，
   标题不匹配会退化成只有一条 Full Changelog 链接。
3. 打 tag 并推送：

```bash
git tag -a vX.Y.Z -m "..."
git push origin main
git push origin vX.Y.Z
```

触发关系：

- **推 tag** → `build-and-release.yml` 全流程：三平台打包 → 发版本 Release（安装包 + GPL 源码包）
  → 更新滚动 release `updater-latest` 的 `latest.json`（含三平台签名）。
- **推 main** → 只有提交信息含 `CI` 才打包；否则 build job 直接 skipped。
- 客户端的「设置 → 更新」读的是
  `releases/download/updater-latest/latest.json`，**不是**版本 Release 的附件列表——
  版本 Release 里本来就没有 `latest.json`，别据此判断发布失败。
