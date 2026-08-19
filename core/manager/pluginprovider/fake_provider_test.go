// 伪供应商子进程测试方案：
// 测试把当前测试二进制复制为 providers/<id>/<entry>，由插件管理器以
// --provider-serve --port 0 拉起；子进程经 TestMain 门控直接进入 fakeProviderMain，
// 按 PLUGIN_TEST_HELPER=<mode> 模拟就绪行各状态（ready/need_config/fatal/崩溃/超时/
// 令牌错误/id 不符）与 127.0.0.1 随机端口 HTTP 服务（/v1/models）。全部本地进程间
// 交互，不触网、不占用固定端口。
package pluginprovider

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"
	"time"
)

// envHelperMode 伪供应商模式（测试进程 env → 经 os.Environ 继承给插件子进程）。
const envHelperMode = "PLUGIN_TEST_HELPER"

// envHelperID 就绪行回显的 id（校验「就绪行 id 与目录一致」用）。
const envHelperID = "PLUGIN_TEST_ID"

func TestMain(m *testing.M) {
	if mode := os.Getenv(envHelperMode); mode != "" {
		fakeProviderMain(mode)
	}
	os.Exit(m.Run())
}

// fakeProviderMain 伪供应商入口（仅以 helper 身份运行的测试进程进入）。
// 注意：凡是需要长期存活的模式都必须保留一个真实 goroutine（这里 = HTTP 服务
// Accept），否则 Go runtime 的 deadlock 检测会在所有 goroutine 阻塞时自杀退出
// （fatal error: all goroutines are asleep）。
func fakeProviderMain(mode string) {
	auth := os.Getenv("PLUGIN_AUTH_TOKEN")
	id := os.Getenv(envHelperID)
	serve := func() net.Listener {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			os.Exit(3)
		}
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"data":[{"id":"m1"},{"id":"m2"}]}`)
				return
			}
			http.NotFound(w, r)
		})}
		go func() { _ = srv.Serve(ln) }() // 监听 socket 已就绪，连接进内核 backlog，无竞态
		return ln
	}
	ln := serve() // 每个模式都起 HTTP 服务：既模拟真实供应商，又防 deadlock 自杀
	readyLine := func(port int) {
		fmt.Printf(`{"state":"ready","port":%d,"auth":%q,"id":%q,"version":"9.9.9"}`+"\n", port, auth, id)
	}
	switch mode {
	case "ready":
		readyLine(ln.Addr().(*net.TCPAddr).Port)
		select {} // 运行中，等待被管理器 kill
	case "crash":
		// 就绪后短暂存活再崩溃 → 管理器指数退避重启。
		readyLine(ln.Addr().(*net.TCPAddr).Port)
		time.Sleep(400 * time.Millisecond)
		os.Exit(1)
	case "need_config":
		fmt.Println(`{"state":"need_config","hint":"请填写 provider_private_configs 中的凭据"}`)
		select {}
	case "need_then_ready":
		// 先 need_config（待配置），sleep 后补打 ready（模拟用户填配置 → 子进程转就绪）。
		// 宿主必须持续消费 stdout 行流才能捕获这条后续 ready（设计文档 §4.1；R5 回归）。
		fmt.Println(`{"state":"need_config","hint":"请填写 provider_private_configs 中的凭据"}`)
		time.Sleep(800 * time.Millisecond)
		readyLine(ln.Addr().(*net.TCPAddr).Port)
		select {}
	case "fatal":
		fmt.Println(`{"state":"fatal","error":"缺少关键配置"}`)
		time.Sleep(300 * time.Millisecond)
		os.Exit(4)
	case "slow":
		select {} // 不输出就绪行 → 启动超时（HTTP 服务 goroutine 保活）
	case "bad_auth":
		// 回显伪造令牌 → 管理器应拒绝（防本地进程冒充）。
		fmt.Println(`{"state":"ready","port":1,"auth":"WRONG-TOKEN","id":"x","version":"1"}`)
		select {}
	case "wrong_id":
		// 回显与目录不一致的 id → 管理器应拒绝。
		fmt.Println(`{"state":"ready","port":1,"auth":"` + auth + `","id":"other-id","version":"1"}`)
		select {}
	}
	os.Exit(0)
}
