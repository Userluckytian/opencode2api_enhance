import { describe, expect, test } from "bun:test"
import {
  aggregateSSE,
  ensureFingerprintTools,
  newSessionID,
  normalizeBody,
  stableSession,
} from "./free-sidecar.mjs"

describe("free-sidecar", () => {
  test("session id matches official shape", () => {
    expect(newSessionID()).toMatch(/^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$/)
  })

  test("stable session is per identity", () => {
    expect(stableSession("a")).toBe(stableSession("a"))
    expect(stableSession("a")).not.toBe(stableSession("b"))
  })

  test("fingerprint tools are replaced with exact definitions", () => {
    const body = {
      tools: [
        {
          type: "function",
          function: { name: "read", description: "stub", parameters: { type: "object" } },
        },
      ],
      tool_choice: "none",
    }
    ensureFingerprintTools(body, false)
    const names = body.tools.map((t) => t.function.name)
    expect(names).toContain("read")
    expect(names).toContain("shell")
    expect(body.tool_choice).toBeUndefined()
    const read = body.tools.find((t) => t.function.name === "read")
    expect(read.function.description).toContain("Read the contents of a file or directory")
  })

  test("chat body gets stream/env/tools", () => {
    const body = { model: "m", stream: false, messages: [{ role: "user", content: "hi" }] }
    const meta = normalizeBody(body, "https://opencode.ai/inference/v1/chat/completions", false)
    expect(meta.isChat).toBe(true)
    expect(body.stream).toBe(true)
    expect(body.messages[0].role).toBe("system")
    expect(body.messages[0].content).toContain("<env>")
    expect(body.tools.length).toBe(2)
  })

  test("aggregate SSE keeps model and content", async () => {
    const sse =
      'data: {"choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}],"usage":{"total_tokens":1}}\n\ndata: [DONE]\n\n'
    const res = new Response(sse, { headers: { "content-type": "text/event-stream" } })
    const out = await aggregateSSE(res, "glm")
    const j = await out.json()
    expect(j.model).toBe("glm")
    expect(j.choices[0].message.content).toBe("hi")
    expect(j.usage.total_tokens).toBe(1)
  })
})
