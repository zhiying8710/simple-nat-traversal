use std::fs;
use std::path::{Path, PathBuf};

use anyhow::{Context, Result, anyhow};

const AUTOSTART_ENTRY_BASENAME: &str = "minipunch-desktop";

#[derive(Debug, Clone)]
pub struct AutostartStatus {
    pub enabled: bool,
    pub entry_path: PathBuf,
    pub platform_label: &'static str,
    pub detail: String,
}

pub fn detect_autostart(config_path: &Path) -> Result<AutostartStatus> {
    let entry_path = autostart_entry_path()?;
    let expected = render_autostart_entry(config_path)?;
    let status = match fs::read_to_string(&entry_path) {
        Ok(existing) if normalize_contents(&existing) == normalize_contents(&expected) => {
            AutostartStatus {
                enabled: true,
                entry_path,
                platform_label: autostart_platform_label(),
                detail: "autostart entry is installed for the current config".to_string(),
            }
        }
        Ok(_) => AutostartStatus {
            enabled: false,
            entry_path,
            platform_label: autostart_platform_label(),
            detail:
                "autostart entry exists but does not match the current config path or launch mode"
                    .to_string(),
        },
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => AutostartStatus {
            enabled: false,
            entry_path,
            platform_label: autostart_platform_label(),
            detail: "autostart entry is not installed".to_string(),
        },
        Err(err) => {
            return Err(err).with_context(|| {
                format!("failed to read autostart entry {}", entry_path.display())
            });
        }
    };
    Ok(status)
}

pub fn enable_autostart(config_path: &Path) -> Result<AutostartStatus> {
    let entry_path = autostart_entry_path()?;
    let entry = render_autostart_entry(config_path)?;
    let parent = entry_path
        .parent()
        .ok_or_else(|| anyhow!("autostart entry path has no parent"))?;
    fs::create_dir_all(parent).with_context(|| format!("failed to create {}", parent.display()))?;
    fs::write(&entry_path, entry)
        .with_context(|| format!("failed to write autostart entry {}", entry_path.display()))?;
    detect_autostart(config_path)
}

pub fn disable_autostart(config_path: &Path) -> Result<AutostartStatus> {
    let entry_path = autostart_entry_path()?;
    match fs::remove_file(&entry_path) {
        Ok(()) => {}
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => {}
        Err(err) => {
            return Err(err).with_context(|| {
                format!("failed to remove autostart entry {}", entry_path.display())
            });
        }
    }
    detect_autostart(config_path)
}

fn autostart_entry_path() -> Result<PathBuf> {
    #[cfg(target_os = "macos")]
    {
        let home = dirs::home_dir().ok_or_else(|| anyhow!("failed to resolve home directory"))?;
        return Ok(home
            .join("Library")
            .join("LaunchAgents")
            .join(format!("{AUTOSTART_ENTRY_BASENAME}.plist")));
    }

    #[cfg(target_os = "linux")]
    {
        let config_dir =
            dirs::config_dir().ok_or_else(|| anyhow!("failed to resolve config directory"))?;
        return Ok(config_dir
            .join("autostart")
            .join(format!("{AUTOSTART_ENTRY_BASENAME}.desktop")));
    }

    #[cfg(target_os = "windows")]
    {
        let data_dir =
            dirs::data_dir().ok_or_else(|| anyhow!("failed to resolve AppData directory"))?;
        return Ok(data_dir
            .join("Microsoft")
            .join("Windows")
            .join("Start Menu")
            .join("Programs")
            .join("Startup")
            .join(format!("{AUTOSTART_ENTRY_BASENAME}.cmd")));
    }

    #[allow(unreachable_code)]
    Err(anyhow!("autostart is not implemented on this platform"))
}

fn render_autostart_entry(config_path: &Path) -> Result<String> {
    let exe_path = absolute_path(std::env::current_exe()?);
    let config_path = absolute_path(config_path.to_path_buf());

    #[cfg(target_os = "macos")]
    {
        return Ok(format!(
            r#"<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>{AUTOSTART_ENTRY_BASENAME}</string>
    <key>ProgramArguments</key>
    <array>
        <string>{}</string>
        <string>--background</string>
        <string>--start-agent</string>
        <string>--config</string>
        <string>{}</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <false/>
</dict>
</plist>
"#,
            xml_escape(&exe_path.to_string_lossy()),
            xml_escape(&config_path.to_string_lossy()),
        ));
    }

    #[cfg(target_os = "linux")]
    {
        return Ok(format!(
            "[Desktop Entry]\nType=Application\nVersion=1.0\nName=MiniPunch Desktop\nComment=MiniPunch lightweight private network desktop\nExec={} --background --start-agent --config {}\nTerminal=false\nHidden=false\nX-GNOME-Autostart-enabled=true\n",
            desktop_quote_arg(&exe_path.to_string_lossy()),
            desktop_quote_arg(&config_path.to_string_lossy()),
        ));
    }

    #[cfg(target_os = "windows")]
    {
        return Ok(format!(
            "@echo off\r\nstart \"\" {} --background --start-agent --config {}\r\n",
            windows_quote_arg(&exe_path.to_string_lossy()),
            windows_quote_arg(&config_path.to_string_lossy()),
        ));
    }

    #[allow(unreachable_code)]
    Err(anyhow!("autostart is not implemented on this platform"))
}

fn autostart_platform_label() -> &'static str {
    #[cfg(target_os = "macos")]
    {
        return "launchd";
    }
    #[cfg(target_os = "linux")]
    {
        return "xdg-autostart";
    }
    #[cfg(target_os = "windows")]
    {
        return "startup-folder";
    }
    #[allow(unreachable_code)]
    "unsupported"
}

fn absolute_path(path: PathBuf) -> PathBuf {
    path.canonicalize().unwrap_or(path)
}

fn normalize_contents(value: &str) -> String {
    value.replace("\r\n", "\n").trim().to_string()
}

fn xml_escape(value: &str) -> String {
    value
        .replace('&', "&amp;")
        .replace('<', "&lt;")
        .replace('>', "&gt;")
        .replace('"', "&quot;")
        .replace('\'', "&apos;")
}

#[cfg(target_os = "linux")]
fn desktop_quote_arg(value: &str) -> String {
    let escaped = value
        .replace('\\', "\\\\")
        .replace('"', "\\\"")
        .replace('`', "\\`")
        .replace('$', "\\$");
    format!("\"{escaped}\"")
}

#[cfg(target_os = "windows")]
fn windows_quote_arg(value: &str) -> String {
    format!("\"{}\"", value.replace('"', "\"\""))
}
