// OpenCode 免费层 Bun sidecar。
//
// 为什么需要它：OpenCode 免费层（Authorization: Bearer public）会校验客户端
// TLS 指纹；官方 opencode CLI 的 Bun/BoringSSL 指纹可过，Go/curl/Node 的
// TLS 栈会被回 429 FreeUsageLimitError。本 sidecar 只负责“用 Bun 发请求”，
// 路由/节点池/重试/统计仍由 Go 网关负责。
//
// 协议（Go rootTransport → sidecar）：
//   POST /proxy
//   X-Oc2a-Target-URL: 原始上游 URL（如 https://opencode.ai/inference/v1/chat/completions）
//   X-Oc2a-Proxy:      可选，HTTP/HTTPS 代理（节点 sing-box 的 http inbound）
//   X-Oc2a-Identity:   可选，稳定 session 的身份键（默认取 authorization）
//   body:              原始上游请求体（JSON）
//
// 返回：上游状态码/头/流式 body 原样透传；sidecar 自身错误返回 502 JSON。

const VERSION = "1.0.0"
const PORT = Number(process.env.FREE_SIDECAR_PORT || 40200)
const HOST = process.env.FREE_SIDECAR_HOST || "127.0.0.1"
const SESSION_TTL_MS = Number(process.env.FREE_SIDECAR_SESSION_TTL_MS || 30 * 60 * 1000)
const MAX_SESSIONS = 1000

const B62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
function randB62(n) {
  const a = new Uint8Array(n)
  crypto.getRandomValues(a)
  let s = ""
  for (const b of a) s += B62[b % 62]
  return s
}

// 与官方 CLI 一致的 ses_ 时间戳格式：~(ms<<12 + counter) 取低 48 位 + 14 位 base62。
export function newSessionID() {
  const ms = Date.now()
  const v = (~((BigInt(ms) << 12n) + 1n)) & ((1n << 48n) - 1n)
  return "ses_" + v.toString(16).padStart(12, "0") + randB62(14)
}

const sessions = new Map() // identity -> { id, lastUsed }
export function stableSession(identity) {
  const key = identity || "default"
  const now = Date.now()
  const hit = sessions.get(key)
  if (hit && now - hit.lastUsed < SESSION_TTL_MS) {
    hit.lastUsed = now
    sessions.delete(key)
    sessions.set(key, hit)
    return hit.id
  }
  const id = newSessionID()
  sessions.set(key, { id, lastUsed: now })
  if (sessions.size > MAX_SESSIONS) {
    const oldest = sessions.keys().next().value
    sessions.delete(oldest)
  }
  return id
}

const SESSION_RE = /^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$/
function pickSession(headers, identity) {
  for (const name of ["x-opencode-session", "x-opencode-session-id", "x-session-affinity", "x-session-id"]) {
    const v = headers.get(name)
    if (v && SESSION_RE.test(v.trim())) return v.trim()
  }
  return stableSession(identity)
}

// 官方 read/shell 的逐字定义（OpenCode v2.0.26 CLI；上游会比对描述与 schema）。
const READ_TOOL = {
  description:
    "Read the contents of a file or directory. Supports text files, images, and PDFs. Images and PDFs are presented directly to the model. Each text line is prefixed by its 1-based line number as <line>: <content>. The prefix is for reference and is not part of the file content. Directory entries are returned one per line. Use offset and limit to read large files or directories in sections. Prefer one larger read over many small slices, and use grep to find specific content in large files.",
  parameters: {
    type: "object",
    properties: {
      path: { type: "string", description: "File or directory to read" },
      offset: {
        type: "integer",
        minimum: 0,
        description: "The line or directory entry to start reading from (1-based)",
      },
      limit: {
        type: "integer",
        minimum: 0,
        description: "The maximum number of lines or directory entries to read (defaults to and capped at 2000)",
      },
    },
    required: ["path"],
    additionalProperties: false,
  },
}
const SHELL_TOOL = {
  description:
    "Execute a shell command and return its output. Commands run on Linux using bash. Quote file paths containing spaces or special characters. Prefer dedicated tools over shell commands when possible. When output is large, the full result is saved to a file and a truncated preview is returned. Rely on automatic truncation unless filtering the output is more useful. Commands accept an optional timeout, background commands have no timeout by default. Background commands return immediately, and you will be notified when they complete.",
  parameters: {
    type: "object",
    properties: {
      command: { type: "string", description: "Shell command string to execute" },
      workdir: {
        type: "string",
        description:
          "Working directory to execute the command in. Defaults to the current working directory. When possible, avoid changing directories in the command and set the working directory here instead.",
      },
      timeout: {
        type: "integer",
        minimum: 0,
        description:
          "Timeout in milliseconds. Set to 0 to disable the timeout. Defaults to 120000 for foreground commands. Background commands have no timeout by default.",
      },
      background: {
        type: "boolean",
        description:
          "Run the command in the background and return immediately (useful for dev servers and long-running builds). You do not need to use '&' at the end of the command when using this parameter. You will be notified when it completes. DO NOT poll for completion.",
      },
    },
    required: ["command"],
    additionalProperties: false,
  },
}

function toolName(t) {
  if (!t || typeof t !== "object") return ""
  if (t.function && typeof t.function === "object" && typeof t.function.name === "string") return t.function.name
  if (typeof t.name === "string") return t.name
  return ""
}

function chatTool(name, def) {
  return { type: "function", function: { name, description: def.description, parameters: def.parameters } }
}
function flatTool(name, def) {
  return { type: "function", name, description: def.description, parameters: def.parameters }
}

export function ensureFingerprintTools(body, flat) {
  const tools = Array.isArray(body.tools) ? body.tools : []
  const kept = tools.filter((t) => toolName(t) !== "read" && toolName(t) !== "shell")
  if (flat) {
    kept.push(flatTool("read", READ_TOOL), flatTool("shell", SHELL_TOOL))
  } else {
    kept.push(chatTool("read", READ_TOOL), chatTool("shell", SHELL_TOOL))
  }
  body.tools = kept
  if (body.tool_choice === "none") delete body.tool_choice
}

const ENV_PROMPT = [
  "Here is some useful information about the environment you are running in:",
  "<env>",
  "  Working directory: .",
  "  Workspace root folder: .",
  "  Is directory a git repo: no",
  `  Platform: ${process.platform}`,
  "</env>",
].join("\n")

function ensureEnv(body, flat) {
  if (flat) {
    if (typeof body.instructions !== "string" || body.instructions.trim() === "") {
      body.instructions = ENV_PROMPT
    }
    return
  }
  const msgs = Array.isArray(body.messages) ? body.messages : []
  const hasSystem = msgs.some((m) => m && (m.role === "system" || m.role === "developer"))
  if (!hasSystem) msgs.unshift({ role: "system", content: ENV_PROMPT })
  body.messages = msgs
}

export function normalizeBody(body, targetUrl, originalStream) {
  const isResponses = /\/responses(?:$|[?#])/.test(targetUrl)
  const isChat = /\/chat\/completions(?:$|[?#])/.test(targetUrl)
  if (isChat) {
    ensureFingerprintTools(body, false)
    ensureEnv(body, false)
    // 免费层只接受流式；非流式请求由本 sidecar 聚合后返回 JSON。
    body.stream = true
  } else if (isResponses) {
    ensureFingerprintTools(body, true)
    ensureEnv(body, true)
    body.stream = true
  }
  return { isChat, isResponses, wantStream: originalStream !== false }
}

function filterResponseHeaders(src) {
  const h = new Headers()
  for (const [k, v] of src.entries()) {
    const lk = k.toLowerCase()
    if (["content-length", "content-encoding", "transfer-encoding", "connection"].includes(lk)) continue
    h.set(k, v)
  }
  return h
}

function sidecarError(status, message, extra = {}) {
  return new Response(
    JSON.stringify({ error: { message, type: "sidecar_error", ...extra } }),
    { status, headers: { "content-type": "application/json" } },
  )
}

// 非流式客户端：把上游 SSE 聚合成 OpenAI chat.completion。
export async function aggregateSSE(res, model) {
  const text = await res.text()
  let content = ""
  let reasoning = ""
  let finish = null
  let usage = null
  const toolCalls = new Map()
  for (const line of text.split(/\r?\n/)) {
    const s = line.trim()
    if (!s.startsWith("data:")) continue
    const data = s.slice(5).trim()
    if (!data || data === "[DONE]") continue
    let j
    try {
      j = JSON.parse(data)
    } catch {
      continue
    }
    if (j.usage) usage = j.usage
    const c = j.choices && j.choices[0]
    if (!c) continue
    if (c.finish_reason) finish = c.finish_reason
    const d = c.delta || {}
    if (typeof d.content === "string") content += d.content
    if (typeof d.reasoning_content === "string") reasoning += d.reasoning_content
    if (Array.isArray(d.tool_calls)) {
      for (const tc of d.tool_calls) {
        const idx = tc.index ?? 0
        const cur = toolCalls.get(idx) || { id: "", type: "function", function: { name: "", arguments: "" } }
        if (tc.id) cur.id = tc.id
        if (tc.function?.name) cur.function.name += tc.function.name
        if (tc.function?.arguments) cur.function.arguments += tc.function.arguments
        toolCalls.set(idx, cur)
      }
    }
  }
  const message = { role: "assistant", content }
  if (reasoning) message.reasoning_content = reasoning
  if (toolCalls.size) message.tool_calls = [...toolCalls.values()]
  const out = {
    id: "chatcmpl-sidecar-" + randB62(12),
    object: "chat.completion",
    created: Math.floor(Date.now() / 1000),
    model: model || "unknown",
    choices: [{ index: 0, message, finish_reason: finish || "stop" }],
  }
  if (usage) out.usage = usage
  return new Response(JSON.stringify(out), {
    status: 200,
    headers: { "content-type": "application/json" },
  })
}

const server = import.meta.main ? Bun.serve({
  hostname: HOST,
  port: PORT,
  idleTimeout: 0, // 流式长连接不设 idle 超时
  async fetch(req) {
    const url = new URL(req.url)
    if (url.pathname === "/health") {
      return Response.json({ ok: true, version: VERSION, sessions: sessions.size })
    }
    if (url.pathname !== "/proxy") {
      return sidecarError(404, "not found")
    }
    if (req.method !== "POST") {
      return sidecarError(405, "method not allowed")
    }

    const target = req.headers.get("x-oc2a-target-url") || ""
    if (!/^https?:\/\//.test(target)) {
      return sidecarError(400, "missing or invalid X-Oc2a-Target-URL")
    }
    const proxy = (req.headers.get("x-oc2a-proxy") || "").trim()
    if (proxy && !/^https?:\/\//.test(proxy)) {
      return sidecarError(400, "X-Oc2a-Proxy must be an HTTP(S) proxy URL (Bun fetch does not support SOCKS)")
    }

    let body
    try {
      body = await req.json()
    } catch (e) {
      return sidecarError(400, "invalid JSON body: " + String(e?.message || e))
    }
    if (!body || typeof body !== "object" || Array.isArray(body)) {
      return sidecarError(400, "body must be a JSON object")
    }

    const originalStream = body.stream
    const meta = normalizeBody(body, target, originalStream)

    const identity =
      req.headers.get("x-oc2a-identity") ||
      req.headers.get("authorization") ||
      req.headers.get("x-api-key") ||
      "default"
    const session = pickSession(req.headers, identity)

    const incomingUA = req.headers.get("user-agent") || ""
    const ua = /^opencode\/[^/]+\/[^/]+\/[^/]+$/.test(incomingUA.trim())
      ? incomingUA.trim()
      : "opencode/latest/2.0.32/cli"
    const project = req.headers.get("x-opencode-project") || "global"

    const headers = {
      authorization: "Bearer public",
      "content-type": "application/json",
      "user-agent": ua,
      "x-opencode-client": "cli",
      "x-opencode-project": project,
      "x-opencode-session": session,
      "x-opencode-session-id": session,
      "x-session-affinity": session,
      "x-session-id": session,
      accept: meta.wantStream ? "text/event-stream" : "application/json",
    }

    const init = { method: "POST", headers, body: JSON.stringify(body) }
    if (proxy) init.proxy = proxy

    let res
    try {
      res = await fetch(target, init)
    } catch (e) {
      return sidecarError(502, "sidecar upstream fetch failed: " + String(e?.message || e), {
        target,
        proxy: proxy || null,
      })
    }

    // 非流式客户端：上游强制 stream=true，这里聚合成 JSON。
    if (!meta.wantStream && (meta.isChat || meta.isResponses)) {
      if ((res.headers.get("content-type") || "").includes("text/event-stream")) {
        return aggregateSSE(res, body.model)
      }
    }

    const outHeaders = filterResponseHeaders(res.headers)
    if (!res.body) {
      return new Response(await res.text(), { status: res.status, headers: outHeaders })
    }
    return new Response(res.body, { status: res.status, headers: outHeaders })
  },
}) : null

if (server) {
  console.log(`[free-sidecar] v${VERSION} listening on http://${HOST}:${PORT}`)
}

function shutdown() {
  console.log("[free-sidecar] shutting down")
  if (server) server.stop(true)
  process.exit(0)
}
if (import.meta.main) {
  process.on("SIGTERM", shutdown)
  process.on("SIGINT", shutdown)
}
