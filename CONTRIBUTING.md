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
