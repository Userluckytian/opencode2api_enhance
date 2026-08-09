fn main() {
    println!("cargo:rustc-check-cfg=cfg(embed_windows_bin)");
    println!("cargo:rustc-check-cfg=cfg(embed_unix_bin)");
    // 按目标平台设置内嵌子程序的源文件名（embed.rs 据此条件编译 include_bytes）。
    // - Windows：bin/opencode2api.exe + bin/sing-box.exe（CI 自行构建/下载）
    // - Linux/macOS：优先 bin/opencode2api + bin/sing-box（平台对应二进制）；
    //   缺失时回退 .exe（仓库默认只有 Windows 版，保证本地可编译）
    let target_os = std::env::var("CARGO_CFG_TARGET_OS").unwrap_or_default();
    match target_os.as_str() {
        "windows" => {
            println!("cargo:rustc-cfg=embed_windows_bin");
        }
        "linux" | "macos" => {
            let root = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("..");
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
    tauri_build::build()
}
