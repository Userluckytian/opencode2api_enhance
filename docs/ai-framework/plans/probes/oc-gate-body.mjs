// 阶段 W T1：opencode 上游 responses 行为探测 —— 请求体生成器（无密钥；Bearer public 免费档）
// 用法: node oc-gate-body.mjs <scenario> [model]
//   scenario: p1_reasoning | p2a_none | p2b_named | p2c_required | p3_image | p4_plain
//   输出: results/body_<scenario>.json（相对本脚本）；stdout 末行打印 ses（探测头需同值）
// 复刻生产形态：门禁四工具真实 schema + stream:true + instructions 占位说明 + prompt_cache_key=ses
// 身份合规：ses_ 前缀按官方 Identifier 时间指纹（encodeIDPrefix(ms,1,descending) 低 48 位）
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = path.dirname(fileURLToPath(import.meta.url))
const scenario = process.argv[2] || 'p4_plain'
const model = process.argv[3] || 'deepseek-v4.1-flash-free'

const rnd = () => Math.random().toString(36).slice(2)
const base62 = (n) => { let s = ''
  while (s.length < n) s += rnd().replace('.', '')
  return s.slice(0, n) }
const ms = BigInt(Date.now())
const low48 = ~((ms << 12n) + 1n) & ((1n << 48n) - 1n)
const ses = 'ses_' + low48.toString(16).padStart(12, '0') + base62(14)

const GATE = {
  shell: { description: 'Execute a shell command and return its output.', parameters: { type: 'object', properties: { command: { type: 'string', description: 'Shell command string to execute' }, workdir: { type: 'string', description: 'Working directory' }, timeout: { type: 'integer', description: 'Timeout in milliseconds' }, background: { type: 'boolean', description: 'Run in background' } }, required: ['command'], additionalProperties: false } },
  glob: { description: 'Search file paths using a glob pattern (examples: "**/*.ts", "src/**/*.tsx").', parameters: { type: 'object', properties: { pattern: { type: 'string', description: 'Glob pattern to match files against' }, path: { type: 'string', description: 'Directory to search. Defaults to current directory.' }, hidden: { type: 'boolean', description: 'Include hidden files (default: false).' }, limit: { type: 'integer', description: 'Max matches (default: 100)' } }, required: ['pattern'], additionalProperties: false } },
  grep: { description: 'Search file contents using ripgrep syntax.', parameters: { type: 'object', properties: { pattern: { type: 'string', minLength: 1, description: 'Regex pattern to match' }, path: { type: 'string', description: 'File or directory to search' }, include: { type: 'string', description: 'Glob filter' }, literal: { type: 'boolean', description: 'Treat pattern as exact text' }, caseSensitive: { type: 'boolean', description: 'Use case-sensitive matching' }, limit: { type: 'integer', description: 'Max matches (default: 100)' } }, required: ['pattern'], additionalProperties: false } },
  read: { description: 'Read the contents of a file or directory.', parameters: { type: 'object', properties: { path: { type: 'string', description: 'File or directory to read' }, offset: { type: 'integer', description: 'Line to start reading from' }, limit: { type: 'integer', description: 'Max lines to read' } }, required: ['path'], additionalProperties: false } },
}
const gateToolsFlat = Object.entries(GATE).map(([name, d]) => ({ type: 'function', name, ...d, strict: false }))
const INSTRUCTIONS = 'The tools named shell, glob, grep and read in this request are placeholders. Do not call them; use the other tools you have been given.'
const WEATHER = { type: 'function', name: 'get_weather', description: 'Get current weather for a city.', parameters: { type: 'object', properties: { city: { type: 'string', description: 'City name' } }, required: ['city'], additionalProperties: false }, strict: false }
// 1x1 红色 PNG（探测 P3 用）
const RED_PX = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAAC0lEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=='

const text = (t) => [{ role: 'user', content: [{ type: 'input_text', text: t }] }]
const base = { model, stream: true, instructions: INSTRUCTIONS, prompt_cache_key: ses, tools: gateToolsFlat }

let body
switch (scenario) {
  case 'p1_reasoning':
    body = { ...base, input: text('A ship is 40m from a cliff moving at 3 knots. Reason step by step when it hits, then answer.'), max_output_tokens: 200, reasoning: { effort: 'medium', summary: 'auto' } }
    break
  case 'p2a_none':
    body = { ...base, input: text('What is the weather in Beijing? If you cannot call tools just say so.'), max_output_tokens: 40, tool_choice: 'none' }
    break
  case 'p2b_named':
    body = { ...base, tools: [...gateToolsFlat, WEATHER], input: text('What is the weather in Beijing?'), max_output_tokens: 60, tool_choice: { type: 'function', name: 'get_weather' } }
    break
  case 'p2c_required':
    body = { ...base, tools: [...gateToolsFlat, WEATHER], input: text('What is the weather in Beijing?'), max_output_tokens: 60, tool_choice: 'required' }
    break
  case 'p3_image':
    body = { ...base, input: [{ role: 'user', content: [{ type: 'input_text', text: 'What color is this image? Answer with exactly one word.' }, { type: 'input_image', image_url: RED_PX }] }], max_output_tokens: 20 }
    break
  case 'p4_plain':
    body = { ...base, input: text('reply with the single word: ok'), max_output_tokens: 16 }
    break
  default:
    console.error('unknown scenario:', scenario)
    process.exit(2)
}

fs.mkdirSync(path.join(HERE, 'results'), { recursive: true })
const out = path.join(HERE, 'results', `body_${scenario}.json`)
fs.writeFileSync(out, JSON.stringify(body))
console.log('body:', out)
console.log(ses)
