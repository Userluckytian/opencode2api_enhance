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

  test("chat body keeps client system prompt and prepends env block", () => {
    const body = {
      model: "m",
      messages: [
        { role: "system", content: "You are a coding agent." },
        { role: "user", content: "hi" },
      ],
    }
    normalizeBody(body, "https://opencode.ai/inference/v1/chat/completions", true)
    expect(body.messages.length).toBe(2)
    const sys = body.messages[0]
    expect(sys.role).toBe("system")
    expect(sys.content).toContain("<env>")
    expect(sys.content).toContain("Working directory:")
    expect(sys.content).toContain("You are a coding agent.")
  })

  test("chat body does not duplicate an existing env block", () => {
    const sys = {
      role: "developer",
      content: "Here is some useful information about the environment you are running in:\n<env>\n  Working directory: .\n</env>\n\nYou are a coding agent.",
    }
    const body = { model: "m", messages: [sys, { role: "user", content: "hi" }] }
    normalizeBody(body, "https://opencode.ai/inference/v1/chat/completions", true)
    expect(body.messages[0].content.match(/<env>/g).length).toBe(1)
  })

  test("chat body handles system content parts", () => {
    const body = {
      model: "m",
      messages: [
        { role: "system", content: [{ type: "text", text: "You are a coding agent." }] },
        { role: "user", content: "hi" },
      ],
    }
    normalizeBody(body, "https://opencode.ai/inference/v1/chat/completions", true)
    const parts = body.messages[0].content
    expect(Array.isArray(parts)).toBe(true)
    expect(parts[0].text).toContain("<env>")
    expect(parts[1].text).toBe("You are a coding agent.")
  })

  test("responses body prepends env block to custom instructions", () => {
    const body = { model: "m", instructions: "You are a coding agent.", input: "hi" }
    normalizeBody(body, "https://opencode.ai/inference/v1/responses", true)
    expect(body.instructions).toContain("<env>")
    expect(body.instructions).toContain("You are a coding agent.")
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
