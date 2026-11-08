use std::io::Read;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::time::Duration;

use crate::models::*;

#[derive(Debug)]
pub enum ClientError {
    BinaryNotFound,
    Launch(String),
    NonZero { code: i32, stderr: String },
    Empty,
    Json(String),
    TimedOut(u64),
}

impl std::fmt::Display for ClientError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::BinaryNotFound => write!(f, "session-pressure not found. Build it or set SESSION_PRESSURE_BIN."),
            Self::Launch(s) => write!(f, "failed to launch session-pressure: {s}"),
            Self::NonZero { code, stderr } => {
                if stderr.trim().is_empty() {
                    write!(f, "session-pressure exited {code}")
                } else {
                    write!(f, "{}", stderr.trim())
                }
            }
            Self::Empty => write!(f, "session-pressure returned empty output"),
            Self::Json(s) => write!(f, "could not decode JSON: {s}"),
            Self::TimedOut(s) => write!(f, "session-pressure did not finish within {s}s"),
        }
    }
}

impl std::error::Error for ClientError {}

#[derive(Debug, Clone)]
pub struct PressureClient {
    pub binary_path: PathBuf,
    timeout: Duration,
}

impl PressureClient {
    pub fn resolve() -> Result<Self, ClientError> {
        let home = std::env::var("HOME").unwrap_or_default();
        let env = |k: &str| std::env::var(k).ok().filter(|s| !s.is_empty());
        let candidates = [
            env("SESSION_PRESSURE_BIN"),
            env("NDEV_PRESSURE_BIN"),
            Some(format!("{home}/.local/bin/session-pressure")),
            Some("/opt/homebrew/bin/session-pressure".into()),
            Some("/usr/local/bin/session-pressure".into()),
            Some(format!("{home}/.local/bin/ndev-pressure")),
            env("NDEV_BIN"),
            Some(format!("{home}/.local/bin/ndev")),
        ];
        for c in candidates.into_iter().flatten() {
            let path = PathBuf::from(c);
            if is_exec(&path) {
                return Ok(Self { binary_path: path, timeout: Duration::from_secs(30) });
            }
        }
        Err(ClientError::BinaryNotFound)
    }

    pub fn board(&self, live: bool, full: bool, include: &[&str]) -> Result<BoardEnvelope, ClientError> {
        let mut args = vec!["board".to_string()];
        if live {
            args.push("--live".into());
        }
        if full {
            args.push("--full".into());
        }
        if !include.is_empty() {
            args.push("--include".into());
            args.push(include.join(","));
        }
        self.run_json(&args, true)
    }

    pub fn work_status(&self) -> Result<BoardEnvelope, ClientError> {
        // work status JSON is a WorkEnvelope; wrap it as a board-shaped value.
        let raw: serde_json::Value = self.run_json_value(&["work".into(), "status".into()], true)?;
        let mut env = BoardEnvelope::default();
        env.ok = raw.get("ok").and_then(|v| v.as_bool()).unwrap_or(true);
        env.work = serde_json::from_value(raw.get("work").cloned().unwrap_or(serde_json::Value::Null)).ok();
        Ok(env)
    }

    pub fn work_inspect(&self, operation_id: &str) -> Result<WorkInspectEnvelope, ClientError> {
        self.run_json(
            &["work".into(), "inspect".into(), "--operation-id".into(), operation_id.into()],
            true,
        )
    }

    pub fn work_history(&self, operation_id: &str) -> Result<WorkHistoryEnvelope, ClientError> {
        self.run_json(
            &[
                "work".into(),
                "history".into(),
                "--since".into(),
                "48h".into(),
                "--limit".into(),
                "200".into(),
                "--operation-id".into(),
                operation_id.into(),
            ],
            true,
        )
    }

    pub fn work_override(&self, operation_id: &str) -> Result<serde_json::Value, ClientError> {
        self.run_json_value(
            &[
                "work".into(),
                "override".into(),
                "--operation-id".into(),
                operation_id.into(),
                "--confirm".into(),
            ],
            true,
        )
    }

    pub fn work_override_all(&self) -> Result<serde_json::Value, ClientError> {
        self.run_json_value(&["work".into(), "override".into(), "--all".into(), "--confirm".into()], true)
    }

    pub fn work_override_clear(&self) -> Result<serde_json::Value, ClientError> {
        self.run_json_value(&["work".into(), "override".into(), "--clear".into(), "--confirm".into()], true)
    }

    pub fn work_cancel(&self, operation_id: &str) -> Result<serde_json::Value, ClientError> {
        self.run_json_value(
            &[
                "work".into(),
                "cancel".into(),
                "--operation-id".into(),
                operation_id.into(),
                "--confirm".into(),
            ],
            true,
        )
    }

    pub fn work_reschedule(&self, operation_id: &str) -> Result<serde_json::Value, ClientError> {
        self.run_json_value(
            &[
                "work".into(),
                "reschedule".into(),
                "--operation-id".into(),
                operation_id.into(),
                "--confirm".into(),
            ],
            true,
        )
    }

    pub fn work_handoff(&self, operation_id: &str) -> Result<serde_json::Value, ClientError> {
        self.run_json_value(
            &["work".into(), "handoff".into(), "--operation-id".into(), operation_id.into()],
            true,
        )
    }

    pub fn policy_profile_apply(&self, profile: &str, auto_shed: bool) -> Result<serde_json::Value, ClientError> {
        let mut args = vec![
            "policy".into(),
            "profile".into(),
            "apply".into(),
            profile.into(),
        ];
        if auto_shed {
            args.push("--with-auto-shed".into());
        }
        self.run_json_value(&args, true)
    }

    pub fn policy_enable(&self, auto_shed: bool) -> Result<serde_json::Value, ClientError> {
        let mut args = vec!["policy".into(), "enable".into()];
        if !auto_shed {
            args.push("--no-auto-shed".into());
        }
        self.run_json_value(&args, true)
    }

    pub fn policy_observe(&self) -> Result<serde_json::Value, ClientError> {
        self.run_json_value(&["policy".into(), "observe".into()], true)
    }

    pub fn monitor_install(&self, enforce: bool) -> Result<serde_json::Value, ClientError> {
        let mut args = vec!["monitor".into(), "install".into()];
        if enforce {
            args.push("--enforce".into());
        }
        self.run_json_value(&args, true)
    }

    pub fn monitor_uninstall(&self) -> Result<serde_json::Value, ClientError> {
        self.run_json_value(&["monitor".into(), "uninstall".into()], true)
    }

    pub fn monitor_once(&self) -> Result<serde_json::Value, ClientError> {
        self.run_json_value(&["monitor".into(), "once".into()], true)
    }

    pub fn idle_apply(&self, root_pid: i64, session_id: &str) -> Result<serde_json::Value, ClientError> {
        self.run_json_value(
            &[
                "idle".into(),
                "--apply".into(),
                "--root-pid".into(),
                root_pid.to_string(),
                "--session-id".into(),
                session_id.into(),
            ],
            true,
        )
    }

    pub fn io_status(&self, live: bool) -> Result<DiskWriteStatusEnvelope, ClientError> {
        let mut args = vec!["io".into(), "status".into()];
        if live {
            args.push("--live".into());
        }
        self.run_json(&args, true)
    }

    pub fn storage_providers(&self) -> Result<StorageProvidersEnvelope, ClientError> {
        self.run_json(&["storage".into(), "providers".into()], true)
    }

    fn run_json<T: serde::de::DeserializeOwned>(&self, leaf: &[String], accept_nonzero: bool) -> Result<T, ClientError> {
        let value = self.run_json_value(leaf, accept_nonzero)?;
        serde_json::from_value(value).map_err(|e| ClientError::Json(e.to_string()))
    }

    fn run_json_value(&self, leaf: &[String], accept_nonzero: bool) -> Result<serde_json::Value, ClientError> {
        let mut args = vec!["--json".into(), "session".into(), "pressure".into()];
        args.extend(leaf.iter().cloned());
        let (code, stdout, stderr) = self.run(&args)?;
        if code != 0 && !accept_nonzero {
            if let Ok(v) = serde_json::from_str::<serde_json::Value>(&stdout) {
                return Ok(v);
            }
            return Err(ClientError::NonZero { code, stderr });
        }
        if stdout.trim().is_empty() {
            if code != 0 {
                return Err(ClientError::NonZero { code, stderr });
            }
            return Err(ClientError::Empty);
        }
        serde_json::from_str(&stdout).or_else(|e| {
            if code != 0 {
                Err(ClientError::NonZero { code, stderr })
            } else {
                Err(ClientError::Json(e.to_string()))
            }
        })
    }

    fn run(&self, args: &[String]) -> Result<(i32, String, String), ClientError> {
        let mut cmd = Command::new(&self.binary_path);
        cmd.args(args)
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped());
        apply_sanitized_env(&mut cmd);
        let mut child = cmd.spawn().map_err(|e| ClientError::Launch(e.to_string()))?;
        // Drain pipes on side threads. A --full board is >64KiB; waiting for
        // exit before read() fills the OS pipe and deadlocks the child.
        let mut stdout_pipe = child.stdout.take();
        let mut stderr_pipe = child.stderr.take();
        let stdout_h = std::thread::spawn(move || {
            let mut buf = String::new();
            if let Some(mut pipe) = stdout_pipe.take() {
                let _ = pipe.read_to_string(&mut buf);
            }
            buf
        });
        let stderr_h = std::thread::spawn(move || {
            let mut buf = String::new();
            if let Some(mut pipe) = stderr_pipe.take() {
                let _ = pipe.read_to_string(&mut buf);
            }
            buf
        });
        let timeout = self.timeout;
        let started = std::time::Instant::now();
        let status = loop {
            match child.try_wait() {
                Ok(Some(status)) => break status,
                Ok(None) => {
                    if started.elapsed() > timeout {
                        let _ = child.kill();
                        let _ = child.wait();
                        return Err(ClientError::TimedOut(timeout.as_secs()));
                    }
                    std::thread::sleep(Duration::from_millis(20));
                }
                Err(e) => return Err(ClientError::Launch(e.to_string())),
            }
        };
        let stdout = stdout_h.join().unwrap_or_default();
        let stderr = stderr_h.join().unwrap_or_default();
        Ok((status.code().unwrap_or(1), stdout, stderr))
    }
}

fn is_exec(path: &Path) -> bool {
    use std::os::unix::fs::PermissionsExt;
    std::fs::metadata(path)
        .map(|m| m.is_file() && m.permissions().mode() & 0o111 != 0)
        .unwrap_or(false)
}

fn apply_sanitized_env(cmd: &mut Command) {
    cmd.env_clear();
    let home = std::env::var("HOME").unwrap_or_default();
    cmd.env("HOME", &home);
    cmd.env(
        "PATH",
        format!("/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:{home}/.local/bin:{home}/tools/session-pressure/bin"),
    );
    if let Ok(tmp) = std::env::var("TMPDIR") {
        cmd.env("TMPDIR", tmp);
    }
    for key in [
        "USER",
        "LOGNAME",
        "LANG",
        "LC_ALL",
        "LC_CTYPE",
        "TERM",
        "SESSION_PRESSURE_BIN",
        "NDEV_PRESSURE_BIN",
        "NDEV_BIN",
        "NDEV_PATH",
        "NDEV_SESSION_PRESSURE_HOME",
        "SESSION_PRESSURE_HOME",
    ] {
        if let Ok(v) = std::env::var(key) {
            if !v.is_empty() {
                cmd.env(key, v);
            }
        }
    }
}
