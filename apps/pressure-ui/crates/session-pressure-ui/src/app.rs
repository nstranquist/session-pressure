use std::time::Duration;

use gpui::prelude::FluentBuilder as _;
use gpui::*;
use gpui_component::{
    ActiveTheme, IconName, Selectable, Sizable, StyledExt, TitleBar,
    button::{Button, ButtonVariants},
    h_flex,
    progress::Progress,
    sidebar::{Sidebar, SidebarGroup, SidebarHeader, SidebarMenu, SidebarMenuItem},
    v_flex,
};
use pressure_core::{
    CommandKind, PressureBoard, PressureClient, PressureLevel, Section, WorkInspectReport,
    WorkLifecycleEvent, WorkSelection, bytes, filter_commands, mb, percent, rate_bps,
    short_id,
};
use smol::Timer;

actions!(
    pressure_ui,
    [
        Quit, Refresh, LiveSample, Palette, ConfirmYes, ConfirmNo, Go1, Go2, Go3, Go4, Go5, Go6, Go7,
        Go8
    ]
);

#[derive(Clone)]
pub enum ConfirmKind {
    StopTree { operation_id: String, summary: String },
    Reschedule { operation_id: String, schedule: String },
    ApplyProfile { profile: String },
}

pub struct PressureApp {
    client: Option<PressureClient>,
    board: PressureBoard,
    section: Section,
    last_error: Option<String>,
    status: Option<String>,
    busy: bool,
    work_selection: Option<WorkSelection>,
    inspect: Option<WorkInspectReport>,
    events: Vec<WorkLifecycleEvent>,
    palette_open: bool,
    palette_query: String,
    palette_index: usize,
    confirm: Option<ConfirmKind>,
    filter: String,
    focus: FocusHandle,
}

impl PressureApp {
    pub fn new(window: &mut Window, cx: &mut Context<Self>) -> Self {
        let client = PressureClient::resolve().ok();
        let mut app = Self {
            client,
            board: PressureBoard::default(),
            section: Section::Overview,
            last_error: None,
            status: None,
            busy: false,
            work_selection: None,
            inspect: None,
            events: Vec::new(),
            palette_open: false,
            palette_query: String::new(),
            palette_index: 0,
            confirm: None,
            filter: String::new(),
            focus: cx.focus_handle(),
        };
        app.kick_refresh(false, cx);
        window.focus(&app.focus, cx);
        cx.spawn(async move |this, cx| {
            let mut ticks = 0u32;
            loop {
                Timer::after(Duration::from_millis(2500)).await;
                ticks = ticks.wrapping_add(1);
                let result = this.update(cx, |this, cx| {
                    if this.section == Section::Work {
                        this.kick_work_focus(cx);
                    }
                    if ticks % 4 == 0 && !this.busy {
                        this.kick_refresh(false, cx);
                    }
                    cx.notify();
                });
                if result.is_err() {
                    break;
                }
            }
        })
        .detach();
        app
    }

    fn kick_refresh(&mut self, live: bool, cx: &mut Context<Self>) {
        let Some(client) = self.client.clone() else {
            self.last_error = Some("session-pressure not found on PATH".into());
            cx.notify();
            return;
        };
        self.busy = true;
        self.status = Some(if live { "Live sample…" } else { "Refreshing…" }.into());
        cx.notify();
        cx.spawn(async move |this, cx| {
            let result = cx
                .background_spawn(async move {
                    client.board(live, true, &["doctor", "calibration", "policy", "monitor", "idle", "telemetry"])
                })
                .await;
            let _ = this.update(cx, |this, cx| {
                this.busy = false;
                match result {
                    Ok(env) => {
                        this.board = PressureBoard::from_envelope(
                            env,
                            this.client.as_ref().map(|c| c.binary_path.display().to_string()).unwrap_or_default(),
                            live,
                        );
                        this.last_error = None;
                        this.status = Some("Live".into());
                        if this.work_selection.is_none() {
                            if let Some(lease) = this.board.work.as_ref().and_then(|w| w.leases.first()).cloned() {
                                this.select_work(
                                    WorkSelection::Lease {
                                        operation_id: lease.op_id().to_string(),
                                        class_name: lease.class_name,
                                        weight: lease.weight,
                                        pid: lease.pid,
                                        lease_id: lease.id,
                                    },
                                    cx,
                                );
                            }
                        }
                    }
                    Err(e) => {
                        this.last_error = Some(e.to_string());
                        this.status = None;
                    }
                }
                cx.notify();
            });
        })
        .detach();
    }

    fn kick_work_focus(&mut self, cx: &mut Context<Self>) {
        let Some(client) = self.client.clone() else { return };
        let selected = self.work_selection.as_ref().map(|s| s.operation_id().to_string());
        cx.spawn(async move |this, cx| {
            let status = cx
                .background_spawn({
                    let client = client.clone();
                    async move { client.work_status() }
                })
                .await;
            let inspect = if let Some(id) = selected.clone() {
                cx.background_spawn(async move { client.work_inspect(&id) })
                    .await
                    .ok()
            } else {
                None
            };
            let _ = this.update(cx, |this, cx| {
                if let Ok(env) = status {
                    if let Some(work) = env.work {
                        this.board.work = Some(work);
                    }
                }
                if let Some(env) = inspect {
                    if let Some(report) = env.inspect {
                        this.inspect = Some(report);
                    }
                }
                cx.notify();
            });
        })
        .detach();
    }

    fn select_section(&mut self, section: Section, cx: &mut Context<Self>) {
        self.section = section;
        self.palette_open = false;
        if section == Section::Work {
            self.kick_work_focus(cx);
        }
        if section == Section::DiskWrites || section == Section::Storage {
            self.kick_refresh(true, cx);
        }
        cx.notify();
    }

    fn select_work(&mut self, selection: WorkSelection, cx: &mut Context<Self>) {
        let id = selection.operation_id().to_string();
        self.work_selection = Some(selection);
        let Some(client) = self.client.clone() else { return };
        cx.spawn(async move |this, cx| {
            let inspect = cx
                .background_spawn({
                    let client = client.clone();
                    let id = id.clone();
                    async move { client.work_inspect(&id) }
                })
                .await;
            let history = cx.background_spawn(async move { client.work_history(&id) }).await;
            let _ = this.update(cx, |this, cx| {
                this.inspect = inspect.ok().and_then(|e| e.inspect);
                this.events = history.map(|h| h.work_events).unwrap_or_default();
                cx.notify();
            });
        })
        .detach();
    }

    fn run_command(&mut self, kind: CommandKind, cx: &mut Context<Self>) {
        match kind {
            CommandKind::GoSection(section) => self.select_section(section, cx),
            CommandKind::Refresh => self.kick_refresh(false, cx),
            CommandKind::LiveSample => self.kick_refresh(true, cx),
            CommandKind::Palette => {
                self.palette_open = !self.palette_open;
                cx.notify();
            }
            CommandKind::RunNow => {
                if let Some(WorkSelection::Waiter { operation_id, .. }) = &self.work_selection {
                    self.mutate_work("override", operation_id.clone(), cx);
                }
            }
            CommandKind::RunAll => self.mutate_simple("override-all", cx),
            CommandKind::ClearPins => self.mutate_simple("override-clear", cx),
            CommandKind::StopTree => {
                if let Some(inspect) = &self.inspect {
                    self.confirm = Some(ConfirmKind::StopTree {
                        operation_id: inspect.operation_id.clone(),
                        summary: inspect.summary.clone(),
                    });
                    cx.notify();
                }
            }
            CommandKind::Reschedule => {
                if let Some(inspect) = &self.inspect {
                    if let Some(sid) = inspect.origin.schedule_id.clone() {
                        self.confirm = Some(ConfirmKind::Reschedule {
                            operation_id: inspect.operation_id.clone(),
                            schedule: sid,
                        });
                        cx.notify();
                    }
                }
            }
            CommandKind::CopyOp => {
                if let Some(sel) = &self.work_selection {
                    cx.write_to_clipboard(ClipboardItem::new_string(sel.operation_id().to_string()));
                    self.status = Some("Copied operation id".into());
                    cx.notify();
                }
            }
            CommandKind::CopyBrief => {
                if let Some(inspect) = &self.inspect {
                    cx.write_to_clipboard(ClipboardItem::new_string(inspect.summary.clone()));
                    self.status = Some("Copied inspect brief".into());
                    cx.notify();
                }
            }
            CommandKind::ApplyBalanced => {
                self.confirm = Some(ConfirmKind::ApplyProfile { profile: "balanced".into() });
                cx.notify();
            }
            CommandKind::ApplyThroughput => {
                self.confirm = Some(ConfirmKind::ApplyProfile { profile: "throughput".into() });
                cx.notify();
            }
            CommandKind::ApplyInteractive => {
                self.confirm = Some(ConfirmKind::ApplyProfile { profile: "interactive".into() });
                cx.notify();
            }
            CommandKind::ApplyObserve => {
                self.confirm = Some(ConfirmKind::ApplyProfile { profile: "observe".into() });
                cx.notify();
            }
        }
    }

    fn run_palette(&mut self, cx: &mut Context<Self>) {
        let items = filter_commands(&self.palette_query);
        if items.is_empty() {
            return;
        }
        let idx = self.palette_index.min(items.len() - 1);
        let kind = items[idx].kind;
        self.palette_open = false;
        self.palette_query.clear();
        self.palette_index = 0;
        self.run_command(kind, cx);
    }

    fn handle_key(&mut self, event: &KeyDownEvent, cx: &mut Context<Self>) {
        if !self.palette_open {
            return;
        }
        let mods = event.keystroke.modifiers;
        let key = event.keystroke.key.as_str();
        match key {
            "escape" => {
                self.palette_open = false;
                self.palette_query.clear();
                cx.notify();
            }
            "enter" => self.run_palette(cx),
            "backspace" => {
                self.palette_query.pop();
                self.palette_index = 0;
                cx.notify();
            }
            "up" => {
                self.palette_index = self.palette_index.saturating_sub(1);
                cx.notify();
            }
            "down" => {
                self.palette_index += 1;
                cx.notify();
            }
            _ if key.len() == 1 && !mods.control && !mods.platform && !mods.alt => {
                self.palette_query.push_str(key);
                self.palette_index = 0;
                cx.notify();
            }
            _ => {}
        }
    }

    fn mutate_work(&mut self, action: &'static str, operation_id: String, cx: &mut Context<Self>) {
        let Some(client) = self.client.clone() else { return };
        self.busy = true;
        self.status = Some(action.into());
        cx.spawn(async move |this, cx| {
            let result = cx
                .background_spawn(async move {
                    match action {
                        "override" => client.work_override(&operation_id),
                        "cancel" => client.work_cancel(&operation_id),
                        "reschedule" => client.work_reschedule(&operation_id),
                        _ => client.work_override(&operation_id),
                    }
                })
                .await;
            let _ = this.update(cx, |this, cx| {
                this.busy = false;
                match result {
                    Ok(_) => {
                        this.status = Some("Done".into());
                        this.kick_refresh(false, cx);
                    }
                    Err(e) => this.last_error = Some(e.to_string()),
                }
                cx.notify();
            });
        })
        .detach();
    }

    fn mutate_simple(&mut self, action: &'static str, cx: &mut Context<Self>) {
        let Some(client) = self.client.clone() else { return };
        self.busy = true;
        cx.spawn(async move |this, cx| {
            let result = cx
                .background_spawn(async move {
                    match action {
                        "override-all" => client.work_override_all(),
                        "override-clear" => client.work_override_clear(),
                        _ => client.work_override_all(),
                    }
                })
                .await;
            let _ = this.update(cx, |this, cx| {
                this.busy = false;
                match result {
                    Ok(_) => {
                        this.status = Some("Done".into());
                        this.kick_refresh(false, cx);
                    }
                    Err(e) => this.last_error = Some(e.to_string()),
                }
                cx.notify();
            });
        })
        .detach();
    }

    fn apply_confirm(&mut self, cx: &mut Context<Self>) {
        match self.confirm.take() {
            Some(ConfirmKind::StopTree { operation_id, .. }) => self.mutate_work("cancel", operation_id, cx),
            Some(ConfirmKind::Reschedule { operation_id, .. }) => self.mutate_work("reschedule", operation_id, cx),
            Some(ConfirmKind::ApplyProfile { profile }) => {
                let Some(client) = self.client.clone() else { return };
                cx.spawn(async move |this, cx| {
                    let result = cx
                        .background_spawn(async move { client.policy_profile_apply(&profile, false) })
                        .await;
                    let _ = this.update(cx, |this, cx| {
                        if let Err(e) = result {
                            this.last_error = Some(e.to_string());
                        } else {
                            this.status = Some("Profile applied".into());
                            this.kick_refresh(false, cx);
                        }
                        cx.notify();
                    });
                })
                .detach();
            }
            None => {}
        }
    }

    fn sidebar_icon(section: Section) -> IconName {
        match section {
            Section::Overview => IconName::LayoutDashboard,
            Section::Trees => IconName::Folder,
            Section::DiskWrites => IconName::HardDrive,
            Section::Storage => IconName::MemoryStick,
            Section::Work => IconName::Inbox,
            Section::Policy => IconName::Settings,
            Section::Monitor => IconName::Cpu,
            Section::Telemetry => IconName::Calendar,
        }
    }

    fn render_sidebar(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let mut menu = SidebarMenu::new();
        for section in Section::all() {
            let s = *section;
            menu = menu.child(
                SidebarMenuItem::new(format!("{}    ⌘{}", section.title(), section.shortcut()))
                    .icon(Self::sidebar_icon(s))
                    .active(self.section == s)
                    .on_click(cx.listener(move |this, _, _, cx| this.select_section(s, cx))),
            );
        }
        Sidebar::new("pressure-nav")
            .w(px(228.))
            .header(
                SidebarHeader::new().child(
                    v_flex()
                        .gap_1()
                        .child(
                            h_flex()
                                .gap_2()
                                .items_center()
                                .child(level_badge(self.board.level(), cx))
                                .child(
                                    v_flex()
                                        .child("Session Pressure")
                                        .child(
                                            div()
                                                .text_xs()
                                                .text_color(cx.theme().muted_foreground)
                                                .child(self.board.policy.as_ref().map(|p| p.mode_label()).unwrap_or_else(|| "—".into())),
                                        ),
                                ),
                        )
                        .child(
                            div()
                                .text_xs()
                                .text_color(cx.theme().muted_foreground)
                                .child(if self.busy { "Refreshing…" } else { "Live" }),
                        ),
                ),
            )
            .child(SidebarGroup::new("Panes").child(menu))
    }

    fn render_status_bar(&self, cx: &Context<Self>) -> impl IntoElement {
        let snap = &self.board.snapshot;
        let work = self.board.work.as_ref();
        let used = work.map(|w| w.used).unwrap_or(0);
        let cap = work.map(|w| w.capacity).unwrap_or(0);
        let depth = work.map(|w| w.queue_depth).unwrap_or(0);
        h_flex()
            .px_3()
            .gap_4()
            .h_8()
            .text_sm()
            .items_center()
            .border_t_1()
            .border_color(cx.theme().border)
            .bg(cx.theme().tab_bar)
            .text_color(cx.theme().muted_foreground)
            .child(level_badge(self.board.level(), cx))
            .child(stat("MEM", percent(snap.memory_used_percent() as f64), snap.memory_used_percent() as f32))
            .child(stat("CPU", format!("{:.0}%", snap.host_cpu_percent), snap.host_cpu_percent as f32))
            .child(stat("CAP", format!("{used}/{cap}"), if cap > 0 { (used as f32 / cap as f32) * 100.0 } else { 0.0 }))
            .child(format!("queue {depth}"))
            .child(format!("trees {}", snap.agent_tree_count))
            .when_some(self.status.clone(), |this, s| this.child(s))
            .when_some(self.last_error.clone(), |this, e| {
                this.child(div().text_color(cx.theme().red).child(e))
            })
    }

    fn render_palette(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let items = filter_commands(&self.palette_query);
        v_flex()
            .id("palette")
            .when(!self.palette_open, |this| this.invisible())
            .absolute()
            .top_16()
            .left_1_2()
            .w(px(520.))
            .ml(px(-260.))
            .p_3()
            .gap_2()
            .rounded(cx.theme().radius)
            .border_1()
            .border_color(cx.theme().border)
            .bg(cx.theme().popover)
            .shadow_lg()
            .child(
                h_flex()
                    .justify_between()
                    .child(div().font_semibold().child("Command palette"))
                    .child(div().text_xs().text_color(cx.theme().muted_foreground).child("⌘K · type to filter")),
            )
            .child(
                div()
                    .text_sm()
                    .text_color(cx.theme().muted_foreground)
                    .child(if self.palette_query.is_empty() {
                        "Refresh · Live sample · Go to Work Queue · Run now · Stop tree".into()
                    } else {
                        format!("filter: {}", self.palette_query)
                    }),
            )
            .children(items.into_iter().take(12).enumerate().map(|(ix, cmd)| {
                let kind = cmd.kind;
                Button::new(cmd.id)
                    .label(format!("{}   {}", cmd.title, cmd.hint))
                    .small()
                    .selected(ix == self.palette_index)
                    .on_click(cx.listener(move |this, _, _, cx| this.run_command(kind, cx)))
            }))
    }

    fn render_confirm(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let (title, body) = match &self.confirm {
            Some(ConfirmKind::StopTree { summary, .. }) => ("Stop this live lease?", summary.clone()),
            Some(ConfirmKind::Reschedule { schedule, .. }) => (
                "Reschedule this job?",
                format!("Cancel the lease, then run {schedule} now."),
            ),
            Some(ConfirmKind::ApplyProfile { profile }) => (
                "Apply work style?",
                format!("Apply the {profile} work style. This is a confirmed policy mutation."),
            ),
            None => ("", String::new()),
        };
        v_flex()
            .id("confirm")
            .when(self.confirm.is_none(), |this| this.invisible())
            .absolute()
            .inset_0()
            .items_center()
            .justify_center()
            .bg(cx.theme().background.opacity(0.55))
            .child(
                v_flex()
                    .w(px(440.))
                    .gap_3()
                    .p_4()
                    .rounded(cx.theme().radius)
                    .border_1()
                    .border_color(cx.theme().border)
                    .bg(cx.theme().popover)
                    .child(div().font_semibold().child(title.to_string()))
                    .child(div().text_sm().child(body))
                    .child(
                        h_flex()
                            .gap_2()
                            .child(
                                Button::new("confirm-yes")
                                    .primary()
                                    .label("Confirm")
                                    .on_click(cx.listener(|this, _, _, cx| this.apply_confirm(cx))),
                            )
                            .child(
                                Button::new("confirm-no")
                                    .label("Cancel")
                                    .on_click(cx.listener(|this, _, _, cx| {
                                        this.confirm = None;
                                        cx.notify();
                                    })),
                            ),
                    ),
            )
    }

    fn render_overview(&self, cx: &Context<Self>) -> impl IntoElement {
        let snap = &self.board.snapshot;
        let work = self.board.work.as_ref();
        v_flex()
            .p_4()
            .gap_4()
            .child(header_row("Host pressure", snap.level.label(), cx))
            .child(
                h_flex()
                    .gap_3()
                    .children([
                        metric("Memory used", percent(snap.memory_used_percent() as f64), cx),
                        metric("Host CPU", format!("{:.0}%", snap.host_cpu_percent), cx),
                        metric("Agent RSS", mb(snap.agent_rss_sum_mb), cx),
                        metric(
                            "Capacity",
                            format!(
                                "{}/{}",
                                work.map(|w| w.used).unwrap_or(0),
                                work.map(|w| w.capacity).unwrap_or(0)
                            ),
                            cx,
                        ),
                    ]),
            )
            .child(
                h_flex()
                    .gap_3()
                    .child(metric(
                        "Admission",
                        if self.board.admission.as_ref().map(|a| a.allowed).unwrap_or(true) {
                            "open".into()
                        } else {
                            "blocked".into()
                        },
                        cx,
                    ))
                    .child(metric(
                        "Disk",
                        snap.disk_write
                            .as_ref()
                            .map(|d| rate_bps(d.bytes_per_second))
                            .unwrap_or_else(|| "—".into()),
                        cx,
                    ))
                    .child(metric(
                        "Volume free",
                        snap.storage
                            .as_ref()
                            .map(|s| format!("{:.1}%", s.free_percent))
                            .unwrap_or_else(|| "—".into()),
                        cx,
                    )),
            )
            .when(!snap.reasons.is_empty(), |this| this.child(card("Why", snap.reasons.clone(), cx)))
            .when_some(self.board.doctor.clone(), |this, doctor| {
                this.when(!doctor.warnings.is_empty(), |this| {
                    this.child(card("Doctor", doctor.warnings.clone(), cx))
                })
            })
            .when(!snap.top_host_consumers.is_empty(), |this| {
                this.child(card(
                    "Top consumers",
                    snap.top_host_consumers
                        .iter()
                        .take(8)
                        .map(|c| {
                            format!(
                                "{}  {}  {:.1}% cpu  {}p",
                                c.executable.clone().unwrap_or_else(|| "—".into()),
                                mb(c.rss_sum_mb),
                                c.cpu_percent,
                                c.process_count
                            )
                        })
                        .collect(),
                    cx,
                ))
            })
            .when(!snap.top_agent_trees.is_empty(), |this| {
                this.child(card(
                    "Agent trees",
                    snap.top_agent_trees
                        .iter()
                        .take(8)
                        .map(|t| {
                            format!(
                                "{} pid {}  {}  {:.1}%  {}",
                                t.agent.clone().unwrap_or_else(|| "agent".into()),
                                t.root_pid,
                                mb(t.rss_sum_mb),
                                t.cpu_percent_sum,
                                t.executable.clone().unwrap_or_default()
                            )
                        })
                        .collect(),
                    cx,
                ))
            })
    }

    fn render_trees(&self, cx: &Context<Self>) -> impl IntoElement {
        let q = self.filter.to_lowercase();
        let mut trees: Vec<_> = self
            .board
            .snapshot
            .top_agent_trees
            .iter()
            .filter(|t| {
                if q.is_empty() {
                    return true;
                }
                t.agent.as_deref().unwrap_or("").to_lowercase().contains(&q)
                    || t.executable.as_deref().unwrap_or("").to_lowercase().contains(&q)
                    || t.root_pid.to_string().contains(&q)
            })
            .cloned()
            .collect();
        trees.sort_by(|a, b| b.rss_sum_mb.partial_cmp(&a.rss_sum_mb).unwrap_or(std::cmp::Ordering::Equal));
        v_flex()
            .p_4()
            .gap_3()
            .child(header_row("Agent trees", &format!("{} shown", trees.len()), cx))
            .children(trees.into_iter().map(|t| {
                h_flex()
                    .gap_3()
                    .p_2()
                    .border_b_1()
                    .border_color(cx.theme().border)
                    .child(div().w(px(90.)).child(t.agent.unwrap_or_else(|| "agent".into())))
                    .child(div().w(px(80.)).child(format!("pid {}", t.root_pid)))
                    .child(div().w(px(80.)).child(mb(t.rss_sum_mb)))
                    .child(div().w(px(70.)).child(format!("{:.1}%", t.cpu_percent_sum)))
                    .child(div().flex_1().child(t.executable.unwrap_or_default()))
                    .child(div().w(px(70.)).child(format!("{}p", t.process_count)))
            }))
    }

    fn render_disk(&self, cx: &Context<Self>) -> impl IntoElement {
        let summary = self.board.snapshot.disk_write.clone().unwrap_or_default();
        v_flex()
            .p_4()
            .gap_3()
            .child(header_row(
                "Disk writes",
                summary.level.as_deref().unwrap_or("—"),
                cx,
            ))
            .child(
                h_flex()
                    .gap_3()
                    .child(metric("Rate", rate_bps(summary.bytes_per_second), cx))
                    .child(metric("15m", bytes(summary.window_15m_bytes), cx))
                    .child(metric("24h", bytes(summary.bytes_24h), cx))
                    .child(metric("Writers", format!("{}", summary.writer_count), cx)),
            )
            .when_some(summary.confidence.clone(), |this, conf| this.child(kv("Confidence", &conf)))
            .when_some(summary.top_writer.clone(), |this, writer| {
                this.child(kv(
                    "Top writer",
                    &format!(
                        "{}  {}",
                        writer.executable.unwrap_or_else(|| "—".into()),
                        rate_bps(writer.bytes_per_second)
                    ),
                ))
            })
            .child(div().text_sm().text_color(cx.theme().muted_foreground).child(
                "Internal SSD rate from the board projection. Path tracing stays a confirmed privileged helper in the Swift app.",
            ))
    }

    fn render_storage(&self, cx: &Context<Self>) -> impl IntoElement {
        let storage = self.board.snapshot.storage.clone().unwrap_or_default();
        v_flex()
            .p_4()
            .gap_3()
            .child(header_row("Storage", storage.level.as_deref().unwrap_or("—"), cx))
            .child(metric("Free", format!("{:.1}%", storage.free_percent), cx))
            .child(metric("Free bytes", bytes(storage.free_bytes), cx))
            .child(metric("Volume", storage.volume_path.clone().unwrap_or_else(|| "—".into()), cx))
            .when_some(self.board.idle_error.clone(), |this, err| {
                this.child(div().text_sm().text_color(cx.theme().yellow).child(err))
            })
            .child(card(
                "Idle trees",
                if self.board.idle_candidates.is_empty() {
                    vec!["No idle candidates in this board (inventory may be stale).".into()]
                } else {
                    self.board
                        .idle_candidates
                        .iter()
                        .take(12)
                        .map(|t| format!("pid {}  {}", t.root_pid, t.executable.clone().unwrap_or_default()))
                        .collect()
                },
                cx,
            ))
    }

    fn render_work(&mut self, cx: &mut Context<Self>) -> impl IntoElement {
        let work = self.board.work.clone().unwrap_or_default();
        h_flex()
            .size_full()
            .child(
                v_flex()
                    .w(px(360.))
                    .h_full()
                    .border_r_1()
                    .border_color(cx.theme().border)
                    .child(header_row(
                        "Queue",
                        &format!("{}/{}  waiters {}", work.used, work.capacity, work.queue_depth),
                        cx,
                    ))
                    .child(
                        div()
                            .px_3()
                            .text_xs()
                            .text_color(cx.theme().muted_foreground)
                            .child(work.decision_reason.clone().unwrap_or_else(|| "queue".into())),
                    )
                    .child(
                        div().id("work-list").flex_1().overflow_y_scroll().p_2().children({
                            let mut rows: Vec<AnyElement> = Vec::new();
                            for lease in work.leases {
                                let sel = WorkSelection::Lease {
                                    operation_id: lease.op_id().to_string(),
                                    class_name: lease.class_name.clone(),
                                    weight: lease.weight,
                                    pid: lease.pid,
                                    lease_id: lease.id.clone(),
                                };
                                let active = self.work_selection.as_ref().map(|s| s.operation_id()) == Some(sel.operation_id());
                                rows.push(work_row("LEASE", &lease.class_name, lease.weight, lease.pid, lease.op_id(), active, sel, cx));
                            }
                            for waiter in work.waiters {
                                let sel = WorkSelection::Waiter {
                                    operation_id: waiter.operation_id.clone(),
                                    class_name: waiter.class_name.clone(),
                                    weight: waiter.weight,
                                    pid: waiter.pid,
                                };
                                let active = self.work_selection.as_ref().map(|s| s.operation_id()) == Some(sel.operation_id());
                                rows.push(work_row("WAIT", &waiter.class_name, waiter.weight, waiter.pid, &waiter.operation_id, active, sel, cx));
                            }
                            for hold in work.admission_holds {
                                rows.push(
                                    div()
                                        .p_2()
                                        .text_xs()
                                        .text_color(cx.theme().yellow)
                                        .child(format!(
                                            "HELD {} w{}  {}",
                                            hold.class_name,
                                            hold.weight,
                                            hold.reason.clone().unwrap_or_default()
                                        ))
                                        .into_any_element(),
                                );
                            }
                            rows
                        }),
                    ),
            )
            .child(
                v_flex()
                    .w(px(340.))
                    .h_full()
                    .border_r_1()
                    .border_color(cx.theme().border)
                    .p_3()
                    .gap_2()
                    .child(header_row("Inspect", "live argv stays here, never in work-events", cx))
                    .when_some(self.inspect.clone(), |this, inspect| {
                        this.child(div().text_sm().child(inspect.summary.clone()))
                            .child(kv("Stall", &inspect.stall_class))
                            .when_some(inspect.command().map(|s| s.to_string()), |d, cmd| d.child(kv("Command", &cmd)))
                            .child(
                                h_flex()
                                    .gap_2()
                                    .when(self.work_selection.as_ref().map(|s| s.is_waiter()).unwrap_or(false), |row| {
                                        row.child(
                                            Button::new("run-now")
                                                .small()
                                                .primary()
                                                .label("Run now")
                                                .on_click(cx.listener(|this, _, _, cx| this.run_command(CommandKind::RunNow, cx))),
                                        )
                                    })
                                    .when(self.work_selection.as_ref().map(|s| s.is_lease()).unwrap_or(false), |row| {
                                        row.child(
                                            Button::new("stop-tree")
                                                .small()
                                                .label("Stop tree")
                                                .on_click(cx.listener(|this, _, _, cx| this.run_command(CommandKind::StopTree, cx))),
                                        )
                                    })
                                    .when(inspect.origin.schedule_id.is_some(), |row| {
                                        row.child(
                                            Button::new("reschedule")
                                                .small()
                                                .label("Reschedule")
                                                .on_click(cx.listener(|this, _, _, cx| this.run_command(CommandKind::Reschedule, cx))),
                                        )
                                    })
                                    .child(
                                        Button::new("copy-brief")
                                            .small()
                                            .label("Copy brief")
                                            .on_click(cx.listener(|this, _, _, cx| this.run_command(CommandKind::CopyBrief, cx))),
                                    )
                                    .child(
                                        Button::new("copy-op")
                                            .small()
                                            .label("Copy id")
                                            .on_click(cx.listener(|this, _, _, cx| this.run_command(CommandKind::CopyOp, cx))),
                                    ),
                            )
                            .children(inspect.suggested_actions.iter().take(4).map(|a| {
                                div().text_xs().text_color(cx.theme().muted_foreground).child(format!(
                                    "{} {}",
                                    a.id,
                                    if a.available { "available" } else { "blocked" }
                                ))
                            }))
                    })
                    .when(self.inspect.is_none(), |this| {
                        this.child(div().text_sm().text_color(cx.theme().muted_foreground).child(
                            "Select a lease or waiter. Inspect is a live sample: command, stall, origin, output tail.",
                        ))
                    })
                    .child(
                        div()
                            .id("lifecycle")
                            .flex_1()
                            .overflow_y_scroll()
                            .children(self.events.iter().rev().take(24).map(|e| {
                                div().text_xs().child(format!(
                                    "{}  {}  {}",
                                    e.event,
                                    e.outcome.clone().unwrap_or_default(),
                                    e.timestamp.clone().unwrap_or_default()
                                ))
                            })),
                    ),
            )
            .child(
                v_flex()
                    .flex_1()
                    .h_full()
                    .p_3()
                    .gap_2()
                    .child(header_row(
                        "Live output",
                        self.inspect
                            .as_ref()
                            .map(|i| i.output.source.as_str())
                            .unwrap_or("none"),
                        cx,
                    ))
                    .child(
                        div()
                            .id("output")
                            .flex_1()
                            .p_3()
                            .rounded(cx.theme().radius)
                            .bg(cx.theme().accent.opacity(0.08))
                            .overflow_y_scroll()
                            .font_family("Geist Mono")
                            .text_sm()
                            .child(
                                self.inspect
                                    .as_ref()
                                    .and_then(|i| i.preview_text().map(|s| s.to_string()))
                                    .unwrap_or_else(|| {
                                        "No preview yet. Scheduled jobs show their log tail. Ad-hoc work run shows the bounded tee once the installed helper is current.".into()
                                    }),
                            ),
                    ),
            )
    }

    fn render_policy(&self, cx: &mut Context<Self>) -> impl IntoElement {
        v_flex()
            .p_4()
            .gap_3()
            .child(header_row(
                "Policy",
                &self.board.policy.as_ref().map(|p| p.mode_label()).unwrap_or_else(|| "—".into()),
                cx,
            ))
            .child(
                h_flex()
                    .gap_2()
                    .child(Button::new("p-bal").label("Balanced").on_click(cx.listener(|this, _, _, cx| this.run_command(CommandKind::ApplyBalanced, cx))))
                    .child(Button::new("p-thr").label("Throughput").on_click(cx.listener(|this, _, _, cx| this.run_command(CommandKind::ApplyThroughput, cx))))
                    .child(Button::new("p-int").label("Interactive").on_click(cx.listener(|this, _, _, cx| this.run_command(CommandKind::ApplyInteractive, cx))))
                    .child(Button::new("p-obs").label("Observe").on_click(cx.listener(|this, _, _, cx| this.run_command(CommandKind::ApplyObserve, cx)))),
            )
            .child(div().text_sm().text_color(cx.theme().muted_foreground).child(
                "Profiles are confirmed. They never bypass host-pressure or capacity. Auto-shed stays an explicit extra confirm in the Swift client; this surface applies the named profile only.",
            ))
            .when_some(self.board.policy.clone(), |this, policy| {
                this.child(kv("Enabled", if policy.enabled { "yes" } else { "no" }))
                    .child(kv("Admission", if policy.enforce_admission { "enforced" } else { "observe" }))
                    .child(kv("Auto-shed", if policy.auto_shed_critical { "on" } else { "off" }))
                    .child(kv("Profile", policy.profile.as_deref().unwrap_or("—")))
            })
            .when_some(self.board.policy_path.clone(), |this, path| this.child(kv("Path", &path)))
            .when_some(self.board.health.clone(), |this, health| {
                this.child(kv("Daily-driver", if health.daily_driver_ready { "ready" } else { "not ready" }))
                    .children(health.operator_reasons.into_iter().take(4).map(|r| {
                        div().text_xs().text_color(cx.theme().muted_foreground).child(r)
                    }))
            })
    }

    fn render_monitor(&self, cx: &Context<Self>) -> impl IntoElement {
        let launchd = self.board.launchd.clone().unwrap_or_default();
        v_flex()
            .p_4()
            .gap_3()
            .child(header_row("Monitor", if launchd.loaded { "loaded" } else { "not loaded" }, cx))
            .child(kv("Label", launchd.label.as_deref().unwrap_or("—")))
            .child(kv("PID", &launchd.pid.map(|p| p.to_string()).unwrap_or_else(|| "—".into())))
            .child(kv("Installed", if launchd.installed { "yes" } else { "no" }))
            .child(kv("Artifact", if launchd.artifact_present.unwrap_or(false) { "present" } else { "missing" }))
            .when_some(self.board.recovery_hint.clone(), |this, hint| {
                this.child(card(
                    "Recovery",
                    vec![hint.reason.unwrap_or_default(), hint.recovery_command.unwrap_or_default()],
                    cx,
                ))
            })
    }

    fn render_telemetry(&self, cx: &Context<Self>) -> impl IntoElement {
        v_flex()
            .p_4()
            .gap_3()
            .child(header_row("Telemetry", &format!("{} events", self.board.telemetry_events.len()), cx))
            .children(self.board.telemetry_events.iter().rev().take(40).map(|e| {
                let level = e
                    .summary
                    .as_ref()
                    .and_then(|s| s.level.clone())
                    .or(e.level.clone())
                    .unwrap_or_default();
                let cpu = e.summary.as_ref().map(|s| s.host_cpu_percent).unwrap_or(0.0);
                let free = e.summary.as_ref().map(|s| s.free_percent).unwrap_or(0);
                div().text_xs().child(format!(
                    "{}  {}  {}  mem {}%  cpu {:.0}%",
                    e.timestamp.clone().unwrap_or_default(),
                    e.event.clone().unwrap_or_default(),
                    level,
                    free,
                    cpu
                ))
            }))
    }
}

impl Focusable for PressureApp {
    fn focus_handle(&self, _: &App) -> FocusHandle {
        self.focus.clone()
    }
}

impl Render for PressureApp {
    fn render(&mut self, _window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        v_flex()
            .size_full()
            .bg(cx.theme().background)
            .track_focus(&self.focus)
            .key_context("PressureApp")
            .on_action(cx.listener(|this, _: &Refresh, _, cx| this.kick_refresh(false, cx)))
            .on_action(cx.listener(|this, _: &LiveSample, _, cx| this.kick_refresh(true, cx)))
            .on_action(cx.listener(|this, _: &Palette, _, cx| {
                this.palette_open = !this.palette_open;
                cx.notify();
            }))
            .on_action(cx.listener(|this, _: &Go1, _, cx| this.select_section(Section::Overview, cx)))
            .on_action(cx.listener(|this, _: &Go2, _, cx| this.select_section(Section::Trees, cx)))
            .on_action(cx.listener(|this, _: &Go3, _, cx| this.select_section(Section::DiskWrites, cx)))
            .on_action(cx.listener(|this, _: &Go4, _, cx| this.select_section(Section::Storage, cx)))
            .on_action(cx.listener(|this, _: &Go5, _, cx| this.select_section(Section::Work, cx)))
            .on_action(cx.listener(|this, _: &Go6, _, cx| this.select_section(Section::Policy, cx)))
            .on_action(cx.listener(|this, _: &Go7, _, cx| this.select_section(Section::Monitor, cx)))
            .on_action(cx.listener(|this, _: &Go8, _, cx| this.select_section(Section::Telemetry, cx)))
            .on_key_down(cx.listener(|this, event: &KeyDownEvent, _, cx| this.handle_key(event, cx)))
            .child(
                TitleBar::new()
                    .child(div().pl_3().font_semibold().child("Session Pressure"))
                    .child(
                        h_flex()
                            .gap_2()
                            .pr_3()
                            .when_some(self.last_error.clone(), |this, e| {
                                this.child(div().text_xs().text_color(cx.theme().red).max_w(px(420.)).child(e))
                            })
                            .child(Button::new("tb-refresh").small().label("Refresh").on_click(cx.listener(|this, _, _, cx| this.kick_refresh(false, cx))))
                            .child(Button::new("tb-live").small().label("Live sample").on_click(cx.listener(|this, _, _, cx| this.kick_refresh(true, cx))))
                            .child(Button::new("tb-palette").small().label("⌘K").on_click(cx.listener(|this, _, _, cx| {
                                this.palette_open = !this.palette_open;
                                cx.notify();
                            }))),
                    ),
            )
            .child(
                h_flex()
                    .flex_1()
                    .min_h_0()
                    .child(self.render_sidebar(cx))
                    .child(
                        div()
                            .id("detail")
                            .flex_1()
                            .min_w_0()
                            .h_full()
                            .overflow_y_scroll()
                            .map(|this| match self.section {
                                Section::Overview => this.child(self.render_overview(cx)),
                                Section::Trees => this.child(self.render_trees(cx)),
                                Section::DiskWrites => this.child(self.render_disk(cx)),
                                Section::Storage => this.child(self.render_storage(cx)),
                                Section::Work => this.child(self.render_work(cx)),
                                Section::Policy => this.child(self.render_policy(cx)),
                                Section::Monitor => this.child(self.render_monitor(cx)),
                                Section::Telemetry => this.child(self.render_telemetry(cx)),
                            }),
                    ),
            )
            .child(self.render_status_bar(cx))
            .child(self.render_palette(cx))
            .child(self.render_confirm(cx))
    }
}

fn level_badge(level: PressureLevel, cx: &Context<PressureApp>) -> impl IntoElement {
    let color = match level {
        PressureLevel::Normal => cx.theme().green,
        PressureLevel::Warning => cx.theme().yellow,
        PressureLevel::Red => cx.theme().red,
        PressureLevel::Critical => cx.theme().red,
        PressureLevel::Unknown => cx.theme().muted_foreground,
    };
    div()
        .px_2()
        .py_0p5()
        .rounded(px(8.))
        .bg(color.opacity(0.18))
        .text_color(color)
        .text_xs()
        .child(level.label().to_string())
}

fn stat(label: &'static str, value: String, pct: f32) -> impl IntoElement {
    h_flex()
        .gap_2()
        .items_center()
        .child(Progress::new(label).w_12().h_2().value(pct.clamp(0.0, 100.0)))
        .child(format!("{label} {value}"))
}

fn metric(title: &'static str, value: String, cx: &Context<PressureApp>) -> impl IntoElement {
    v_flex()
        .flex_1()
        .p_3()
        .gap_1()
        .rounded(cx.theme().radius)
        .border_1()
        .border_color(cx.theme().border)
        .child(div().text_xs().text_color(cx.theme().muted_foreground).child(title))
        .child(div().text_lg().font_semibold().child(value))
}

fn card(title: &'static str, lines: Vec<String>, cx: &Context<PressureApp>) -> impl IntoElement {
    v_flex()
        .gap_1()
        .p_3()
        .rounded(cx.theme().radius)
        .border_1()
        .border_color(cx.theme().border)
        .child(div().text_sm().font_semibold().child(title))
        .children(lines.into_iter().map(|l| div().text_sm().child(l)))
}

fn kv(k: &'static str, v: &str) -> impl IntoElement {
    h_flex().gap_2().child(div().w(px(90.)).text_xs().child(k)).child(div().text_sm().child(v.to_string()))
}

fn header_row(title: &'static str, subtitle: &str, cx: &Context<PressureApp>) -> impl IntoElement {
    h_flex()
        .px_3()
        .pt_3()
        .justify_between()
        .child(div().text_lg().font_semibold().child(title))
        .child(div().text_xs().text_color(cx.theme().muted_foreground).child(subtitle.to_string()))
}

fn work_row(
    kind: &'static str,
    class_name: &str,
    weight: i64,
    pid: Option<i64>,
    operation_id: &str,
    active: bool,
    sel: WorkSelection,
    cx: &mut Context<PressureApp>,
) -> AnyElement {
    Button::new(operation_id.to_string())
        .selected(active)
        .label(format!(
            "{kind}  {class_name} w{weight}  pid {}  {}",
            pid.map(|p| p.to_string()).unwrap_or_else(|| "—".into()),
            short_id(operation_id)
        ))
        .on_click(cx.listener(move |this, _, _, cx| this.select_work(sel.clone(), cx)))
        .into_any_element()
}
