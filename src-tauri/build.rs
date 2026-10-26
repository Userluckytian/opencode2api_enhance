fn main() {
    println!("cargo:rustc-check-cfg=cfg(embed_windows_bin)");
    println!("cargo:rustc-check-cfg=cfg(embed_unix_bin)");
    println!("cargo:rustc-check-cfg=cfg(embed_free_sidecar)");
    // 按目标平台设置内嵌子程序的源文件名（embed.rs 据此条件编译 include_bytes）。
    // - Windows：bin/opencode2api.exe + bin/sing-box.exe（CI 自行构建/下载）
    // - Linux/macOS：优先 bin/opencode2api + bin/sing-box（平台对应二进制）；
    //   缺失时回退 .exe（仓库默认只有 Windows 版，保证本地可编译）
    let root = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("..");
    let target_os = std::env::var("CARGO_CFG_TARGET_OS").unwrap_or_default();
    match target_os.as_str() {
        "windows" => {
            println!("cargo:rustc-cfg=embed_windows_bin");
        }
        "linux" | "macos" => {
            let oc = root.join("bin/opencode2api");
            let sb = root.join("bin/sing-box");
            if oc.exists() && sb.exists() {
                println!("cargo:rustc-cfg=embed_unix_bin");
            } else {
                println!("cargo:rustc-cfg=embed_windows_bin");
            }
        }
        _ => {
            println!("cargo:rustc-cfg=embed_windows_bin");
        }
    }
    // 免费层 Bun sidecar：存在才内嵌（scripts/build-free-sidecar.* 产物）。
    // 缺失不阻断编译——管理器会回退旧直连/代理池路径并告警。
    let sidecar = if target_os == "windows" {
        root.join("bin/free-sidecar.exe")
    } else {
        root.join("bin/free-sidecar")
    };
    // 无条件登记变更：先缺文件构建过一次的工程，后续放入 sidecar 也能触发重编。
    println!("cargo:rerun-if-changed={}", sidecar.display());
    if sidecar.exists() {
        println!("cargo:rustc-cfg=embed_free_sidecar");
    } else {
        println!("cargo:warning=bin/free-sidecar not found; free-tier Bun sidecar will not be bundled (run scripts/build-free-sidecar.ps1|sh)");
    }
    tauri_build::build()
}
