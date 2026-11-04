// 本文件把「core 注入厂商的运行时参数键」收敛到 contract 包，
// 让 vendors/* 之间互不依赖（历史上 custom/remote 需要 import vendors/opencode
// 才能拿到 ParamTransport 常量，形成厂商间横向依赖）。
package contract

// ParamTransport 是 core 注入厂商的 Transport 实例键。
//
// 该键以 "_" 开头，表示「运行时注入、不落配置文件」——core 装配厂商时通过
// ProviderSpec.Params 传入，配置里的 providers[].params 同名键会被忽略
// （见 mergeVendorParams 对 "_" 前缀的处理）。
const ParamTransport = "_transport"

// ParamAdminPassword 是管理面板密码（厂商需要据此构造管理域请求头）。
const ParamAdminPassword = "_admin_password"

// 以下是 core → 厂商的 Message.Extra 键（contract.Message.Extra 厂商私有区）。
// core 只负责搬运，不解释语义；厂商按需读取。
const (
	// ParamRawBody 原始入站 OpenAI 请求体（[]byte）。保留 tools/options 等完整字段，
	// 优于用归一化 Messages 重建。
	ParamRawBody = "_oc_raw_body"
	// ParamAuthMode 入站凭据模式字符串（"public" | "auto"）。
	ParamAuthMode = "_oc_auth_mode"
	// ParamAuthToken 入站凭据原文（不含 "Bearer " 前缀）。
	ParamAuthToken = "_oc_auth_token"
	// ParamMaxRetries 厂商级最大重试次数（int）。
	ParamMaxRetries = "_oc_max_retries"
	// ParamProbe 本次调用是否为探针（bool）：探针应跳过退避重试，快速拿到真实状态码。
	ParamProbe = "_oc_probe"
)
