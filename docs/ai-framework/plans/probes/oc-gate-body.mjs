// 生成探测材料：合规 ses_ ID（官方时间指纹）+ chat/responses 两种门禁 body
import fs from 'node:fs'
const T = (n) => 'C:/Users/ASUS/AppData/Local/Temp/' + n
const rnd = () => Math.random().toString(36).slice(2)
const base62 = (n) => { let s=''
  while (s.length < n) s += rnd().replace('.', '')
  return s.slice(0, n) }
const ms = BigInt(Date.now())
const v = (ms << 12n) + 1n                       // 官方 encodeIDPrefix(ms, 1, descending)
const low48 = (~v & 0xFFFFFFFFFFFFFFFFn) & ((1n << 48n) - 1n)
const ses = 'ses_' + low48.toString(16).padStart(12, '0') + base62(14)

const gateToolsChat = [
  { type:'function', function:{ name:'shell', description:'Execute a shell command and return its output.', parameters:{ type:'object', properties:{ command:{type:'string',description:'Shell command string to execute'}, workdir:{type:'string',description:'Working directory'}, timeout:{type:'integer',description:'Timeout in milliseconds'}, background:{type:'boolean',description:'Run in background'} }, required:['command'], additionalProperties:false }, strict:false } },
  { type:'function', function:{ name:'glob', description:'Search file paths using a glob pattern (examples: "**/*.ts", "src/**/*.tsx").', parameters:{ type:'object', properties:{ pattern:{type:'string',description:'Glob pattern to match files against'}, path:{type:'string',description:'Directory to search. Defaults to current directory.'}, hidden:{type:'boolean',description:'Include hidden files (default: false).'}, limit:{type:'integer',description:'Max matches (default: 100)'} }, required:['pattern'], additionalProperties:false }, strict:false } },
  { type:'function', function:{ name:'grep', description:'Search file contents using ripgrep syntax.', parameters:{ type:'object', properties:{ pattern:{type:'string',minLength:1,description:'Regex pattern to match'}, path:{type:'string',description:'File or directory to search'}, include:{type:'string',description:'Glob filter'}, literal:{type:'boolean',description:'Treat pattern as exact text'}, caseSensitive:{type:'boolean',description:'Use case-sensitive matching'}, limit:{type:'integer',description:'Max matches (default: 100)'} }, required:['pattern'], additionalProperties:false }, strict:false } },
  { type:'function', function:{ name:'read', description:'Read the contents of a file or directory.', parameters:{ type:'object', properties:{ path:{type:'string',description:'File or directory to read'}, offset:{type:'integer',description:'Line to start reading from'}, limit:{type:'integer',description:'Max lines to read'} }, required:['path'], additionalProperties:false }, strict:false } },
]
const gateToolsFlat = gateToolsChat.map(t => ({ type:'function', name:t.function.name, description:t.function.description, parameters:t.function.parameters, strict:false }))

const model = process.argv[2] || 'deepseek-v4.1-flash-free'
fs.writeFileSync(T('body_chat.json'), JSON.stringify({ model, messages:[{role:'user',content:'reply with the single word: ok'}], stream:true, max_tokens:16, tools:gateToolsChat, prompt_cache_key:ses }))
fs.writeFileSync(T('body_resp.json'), JSON.stringify({ model, input:[{role:'user',content:[{type:'input_text',text:'reply with the single word: ok'}]}], stream:true, max_output_tokens:16, tools:gateToolsFlat, prompt_cache_key:ses }))
fs.writeFileSync(T('ses.txt'), ses)
console.log('ses:', ses)
