#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
vision-bridge.py — 本地图片 → 视觉模型（OpenAI 兼容端点）文字描述桥。

供 OpenCode 子代理 vision-analyst 使用：主力模型（通常无视觉能力）把图片
分析任务交给子代理，子代理运行本脚本把图片发给视觉模型，拿回文字描述。

用法:
    python vision-bridge.py <image_path> [--prompt "问题"] [--max-tokens 500]

环境变量（均可选，有默认值）:
    VISION_API_BASE  端点，默认 http://127.0.0.1:18080/v1
    VISION_API_KEY   API key，默认空
    VISION_MODEL     模型名，默认 mimo-v2.5

设计约束（符合 ai-framework 三原则）:
    - 幂等：同一图片+prompt 输出稳定，无副作用
    - 可验证：stdout 即结果，非零退出码=失败
    - 零第三方依赖：仅标准库（urllib）

退出码: 0 成功（描述输出到 stdout）；1 失败（错误信息输出到 stderr）。
"""

import argparse
import base64
import json
import os
import sys
import urllib.request

IMAGE_EXTS = {".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp"}


def main() -> int:
    parser = argparse.ArgumentParser(description="图片 → 视觉模型文字描述桥")
    parser.add_argument("image", help="本地图片路径")
    parser.add_argument("--prompt", default="用中文详细描述这张图片的内容。",
                        help="要问视觉模型的问题")
    parser.add_argument("--max-tokens", type=int, default=2000)
    args = parser.parse_args()

    # 1. 校验图片
    img_path = args.image
    if not os.path.isfile(img_path):
        print(f"ERROR: 图片不存在: {img_path}", file=sys.stderr)
        return 1
    ext = os.path.splitext(img_path)[1].lower()
    if ext not in IMAGE_EXTS:
        print(f"ERROR: 不支持的图片格式 {ext}（支持: {sorted(IMAGE_EXTS)}）", file=sys.stderr)
        return 1

    # 2. 组装请求
    api_base = os.environ.get("VISION_API_BASE", "http://127.0.0.1:18080/v1").rstrip("/")
    api_key = os.environ.get("VISION_API_KEY", "")
    model = os.environ.get("VISION_MODEL", "mimo-v2.5")

    with open(img_path, "rb") as f:
        b64 = base64.b64encode(f.read()).decode("ascii")

    payload = {
        "model": model,
        "messages": [{
            "role": "user",
            "content": [
                {"type": "text", "text": args.prompt},
                {"type": "image_url",
                 "image_url": {"url": f"data:image/{ext.lstrip('.')};base64,{b64}"}},
            ],
        }],
        "max_tokens": args.max_tokens,
    }

    req = urllib.request.Request(
        api_base + "/chat/completions",
        data=json.dumps(payload).encode("utf-8"),
        headers={
            "Content-Type": "application/json",
            **({"Authorization": f"Bearer {api_key}"} if api_key else {}),
        },
    )

    # 3. 调用并输出
    try:
        with urllib.request.urlopen(req, timeout=180) as resp:
            data = json.loads(resp.read().decode("utf-8"))
    except Exception as e:
        body = ""
        if hasattr(e, "read"):
            try:
                body = e.read().decode("utf-8")[:500]
            except Exception:
                pass
        print(f"ERROR: API 调用失败: {e} {body}", file=sys.stderr)
        return 1

    if "choices" not in data or not data["choices"]:
        print(f"ERROR: 响应异常: {json.dumps(data, ensure_ascii=False)[:500]}", file=sys.stderr)
        return 1

    msg = data["choices"][0].get("message", {})
    content = (msg.get("content") or "").strip()
    reasoning = (msg.get("reasoning") or "").strip()
    finish = data["choices"][0].get("finish_reason")

    if not content and reasoning:
        # 推理模型把配额吃光时 content 可能为空：用 reasoning 兜底
        content = "[模型仅输出思考过程]\n" + reasoning
    if finish == "length":
        print("WARN: 输出被 max_tokens 截断，可增大 --max-tokens", file=sys.stderr)

    print(content)
    return 0


if __name__ == "__main__":
    sys.exit(main())
