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
