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
    AlertItem, CommandKind, PressureBoard, PressureClient, PressureLevel, Section,
    WorkInspectReport, WorkLifecycleEvent, WorkMemoryHeadroom, WorkPlacementReport, WorkSelection,
    bytes, duration_ms, filter_commands, mb, percent, rate_bps, short_id, swap_out_rate,
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
    ClearRecoveryHint,
}

pub struct PressureApp {
    client: Option<PressureClient>,
    board: PressureBoard,
    work_headroom: Option<WorkMemoryHeadroom>,
    work_headroom_error: Option<String>,
    work_placement: Option<WorkPlacementReport>,
    disk_health_error: Option<String>,
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
            work_headroom: None,
            work_headroom_error: None,
            work_placement: None,
            disk_health_error: None,
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
            let board_client = client.clone();
            let headroom_client = client.clone();
            let placement_client = client.clone();
            let result = cx
                .background_spawn(async move {
                    board_client.board(live, true, &["doctor", "calibration", "policy", "monitor", "idle", "telemetry"])
                })
                .await;
            let headroom = cx
                .background_spawn(async move { headroom_client.work_headroom() })
                .await;
            let placement = cx
                .background_spawn(async move { placement_client.work_placement("6h") })
                .await;
            let _ = this.update(cx, |this, cx| {
                this.busy = false;
                match headroom {
                    Ok(env) => {
                        this.work_headroom = env.headroom;
                        this.work_headroom_error = None;
                    }
                    Err(e) => {
                        this.work_headroom_error = Some(e.to_string());
                    }
                }
                if let Ok(env) = placement {
                    this.work_placement = env.placement;
                }
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
            let headroom = cx
                .background_spawn({
                    let client = client.clone();
                    async move { client.work_headroom() }
                })
                .await;
            let placement = cx
                .background_spawn({
                    let client = client.clone();
                    async move { client.work_placement("6h") }
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
                match headroom {
                    Ok(env) => {
                        this.work_headroom = env.headroom;
                        this.work_headroom_error = None;
                    }
                    Err(e) => {
                        this.work_headroom_error = Some(e.to_string());
                    }
                }
                if let Ok(env) = placement {
                    this.work_placement = env.placement;
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
            Some(ConfirmKind::ClearRecoveryHint) => {
                let Some(client) = self.client.clone() else { return };
                cx.spawn(async move |this, cx| {
                    let result = cx
                        .background_spawn(async move { client.recovery_clear() })
                        .await;
                    let _ = this.update(cx, |this, cx| {
                        if let Err(e) = result {
                            this.last_error = Some(e.to_string());
                        } else {
                            this.status = Some("Recovery hint cleared".into());
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

    fn current_alerts(&self) -> Vec<AlertItem> {
        self.board
            .alerts(self.work_placement.as_ref(), self.disk_health_error.as_deref())
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
        let resource_mode = work.map(|w| w.is_resource_admission()).unwrap_or(false);
        let (cap_label, cap_value, cap_pct) = if resource_mode {
            if let Some(h) = &self.work_headroom {
                (
                    "HEADROOM",
                    if h.sample_fresh {
                        mb(h.headroom_mb.max(0) as f64)
                    } else {
                        "stale".into()
                    },
                    h.used_fraction() * 100.0,
                )
            } else {
                let active = work.map(|w| w.active_lease_count()).unwrap_or(0);
                ("WORK", format!("{active} active"), 0.0)
            }
        } else {
            (
                "CAP",
                format!("{used}/{cap}"),
                if cap > 0 { (used as f32 / cap as f32) * 100.0 } else { 0.0 },
            )
        };
        let alert_count = self.current_alerts().len();
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
            .child(stat(cap_label, cap_value, cap_pct))
            .child(format!("queue {depth}"))
            .child(format!("trees {}", snap.agent_tree_count))
            .when(alert_count > 0, |this| {
                this.child(div().text_color(cx.theme().yellow).child(format!("{alert_count} alert(s)")))
            })
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
            Some(ConfirmKind::ClearRecoveryHint) => (
                "Clear recovery hint?",
                "Removes the pending unclean-shutdown recovery hint so doctor and status return to normal.".into(),
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

    fn render_overview(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let snap = &self.board.snapshot;
        let work = self.board.work.as_ref();
        let alerts = self.current_alerts();
        let resource_mode = work.map(|w| w.is_resource_admission()).unwrap_or(false);
        let work_metric_title = if resource_mode { "Work headroom" } else { "Capacity" };
        let work_metric_value = if resource_mode {
            if let Some(h) = &self.work_headroom {
                if h.sample_fresh {
                    format!("{} free", mb(h.headroom_mb.max(0) as f64))
                } else {
                    "Sample stale".into()
                }
            } else if let Some(w) = work {
                let parked = w.parked_lease_count();
                if parked > 0 {
                    format!("{} active · {} parked · q {}", w.active_lease_count(), parked, w.queue_depth)
                } else {
                    format!("{} active · q {}", w.active_lease_count(), w.queue_depth)
                }
            } else {
                "—".into()
            }
        } else {
            format!(
                "{}/{}",
                work.map(|w| w.used).unwrap_or(0),
                work.map(|w| w.capacity).unwrap_or(0)
            )
        };
        let work_metric_level = if resource_mode {
            if let Some(h) = &self.work_headroom {
                if h.headroom_mb <= 0 {
                    PressureLevel::Red
                } else if h.swap_thrash_latched {
                    PressureLevel::Warning
                } else {
                    PressureLevel::Normal
                }
            } else {
                PressureLevel::Normal
            }
        } else {
            PressureLevel::Normal
        };
        let swap_value = if let Some(rate) = snap.swap_out_mb_per_s {
            if snap.swap_thrash_latched {
                format!("{} · {} out (thrash)", mb(snap.swap_used_mb), swap_out_rate(rate))
            } else {
                format!("{} · {} out", mb(snap.swap_used_mb), swap_out_rate(rate))
            }
        } else {
            mb(snap.swap_used_mb)
        };
        v_flex()
            .p_4()
            .gap_4()
            .child(header_row("Host pressure", snap.level.label(), cx))
            .when(!alerts.is_empty(), |this| {
                this.child(
                    v_flex()
                        .gap_1()
                        .p_3()
                        .rounded(cx.theme().radius)
                        .border_1()
                        .border_color(cx.theme().yellow)
                        .child(div().text_sm().font_semibold().child(format!("Alerts ({})", alerts.len())))
                        .children(alerts.into_iter().map(|alert| {
                            let target = alert.section;
                            let id = alert.id.clone();
                            Button::new(format!("alert-{id}"))
                                .small()
                                .label(format!(
                                    "[{}] {} — {} → {}",
                                    alert.severity.label(),
                                    alert.title,
                                    alert.detail,
                                    target.title()
                                ))
                                .on_click(cx.listener(move |this, _, _, cx| this.select_section(target, cx)))
                        })),
                )
            })
            .child(
                h_flex()
                    .gap_3()
                    .children([
                        metric_leveled(
                            "Memory used",
                            percent(snap.memory_used_percent() as f64),
                            self.board.free_memory_level(),
                            cx,
                        ),
                        metric_leveled(
                            "Host CPU",
                            format!("{:.0}%", snap.host_cpu_percent),
                            self.board.host_cpu_level(),
                            cx,
                        ),
                        metric_leveled(
                            "Agent RSS",
                            format!(
                                "{} / {}",
                                mb(snap.agent_rss_sum_mb),
                                mb(self.board.agent_rss_ceiling_mb())
                            ),
                            self.board.agent_rss_level(),
                            cx,
                        ),
                        metric_leveled(work_metric_title, work_metric_value, work_metric_level, cx),
                    ]),
            )
            .child(
                h_flex()
                    .gap_3()
                    .child(metric_leveled(
                        "Swap",
                        swap_value,
                        self.board.swap_level(),
                        cx,
                    ))
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
                let mut lines = doctor.warnings.clone();
                if let Some(dw) = &doctor.work {
                    let work_line = if dw.is_resource_admission() {
                        format!("work: resource mode · depth {}", dw.queue_depth.unwrap_or(0))
                    } else {
                        format!(
                            "work: queue {}/{} depth {}",
                            dw.used.unwrap_or(0),
                            dw.capacity.unwrap_or(0),
                            dw.queue_depth.unwrap_or(0)
                        )
                    };
                    lines.push(work_line);
                }
                this.when(!lines.is_empty(), |this| this.child(card("Doctor", lines, cx)))
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
        let resource_mode = work.is_resource_admission();
        let queue_subtitle = if resource_mode {
            format!(
                "{} active · {} parked · q {}",
                work.active_lease_count(),
                work.parked_lease_count(),
                work.queue_depth
            )
        } else {
            format!("{}/{}  waiters {}", work.used, work.capacity, work.queue_depth)
        };
        let active_stall = self.work_placement.as_ref().and_then(|p| p.active_stall.clone());
        let parked_stalls = self
            .work_placement
            .as_ref()
            .map(|p| p.parked_stalls.clone())
            .unwrap_or_default();
        let selected_waiter = match &self.work_selection {
            Some(WorkSelection::Waiter { operation_id, .. }) => {
                work.waiters.iter().find(|w| w.operation_id == *operation_id).cloned()
            }
            _ => None,
        };
        let selected_waiter_pressure_reserved = selected_waiter
            .as_ref()
            .map(|w| w.is_pressure_reserved())
            .unwrap_or(false);
        h_flex()
            .size_full()
            .child(
                v_flex()
                    .w(px(360.))
                    .h_full()
                    .border_r_1()
                    .border_color(cx.theme().border)
                    .child(header_row("Queue", &queue_subtitle, cx))
                    .when(resource_mode, |this| {
                        this.child(
                            v_flex()
                                .px_3()
                                .py_2()
                                .gap_1()
                                .border_b_1()
                                .border_color(cx.theme().border)
                                .when_some(self.work_headroom.clone(), |d, h| {
                                    let clamped = h.headroom_mb.max(0);
                                    let headline = if h.sample_fresh {
                                        format!("{} headroom", mb(clamped as f64))
                                    } else {
                                        "Sample stale".into()
                                    };
                                    let color = if h.headroom_mb <= 0 {
                                        cx.theme().red
                                    } else if h.swap_thrash_latched {
                                        cx.theme().yellow
                                    } else {
                                        cx.theme().foreground
                                    };
                                    d.child(div().text_sm().font_semibold().text_color(color).child(headline))
                                        .child(Progress::new("work-headroom").h_2().value(h.used_fraction() * 100.0))
                                        .child(
                                            div()
                                                .text_xs()
                                                .text_color(cx.theme().muted_foreground)
                                                .child(format!(
                                                    "avail {} − reserve {} − in-flight {} · swap-out {}",
                                                    mb(h.available_mb as f64),
                                                    mb(h.reserve_mb as f64),
                                                    mb(h.inflight_mb as f64),
                                                    swap_out_rate(h.swap_out_mb_per_s)
                                                )),
                                        )
                                })
                                .when(self.work_headroom.is_none(), |d| {
                                    d.child(
                                        div()
                                            .text_xs()
                                            .text_color(cx.theme().muted_foreground)
                                            .child(
                                                self.work_headroom_error
                                                    .clone()
                                                    .unwrap_or_else(|| "Loading memory headroom…".into()),
                                            ),
                                    )
                                }),
                        )
                    })
                    .when_some(active_stall, |this, stall| {
                        let cls = stall.class_name.unwrap_or_else(|| "work".into());
                        let reason = stall
                            .decision_reason
                            .filter(|s| !s.is_empty())
                            .map(|r| format!(" · {r}"))
                            .unwrap_or_default();
                        this.child(
                            div()
                                .mx_2()
                                .mt_2()
                                .p_2()
                                .rounded(cx.theme().radius)
                                .border_1()
                                .border_color(cx.theme().yellow)
                                .text_xs()
                                .text_color(cx.theme().yellow)
                                .child(format!(
                                    "STALL: {cls} stalled {}{reason} ({})",
                                    duration_ms(stall.duration_ms),
                                    short_id(&stall.operation_id)
                                )),
                        )
                    })
                    .when(!parked_stalls.is_empty(), |this| {
                        this.children(parked_stalls.into_iter().take(3).map(|p| {
                            let cls = p.class_name.unwrap_or_else(|| "work".into());
                            div()
                                .mx_2()
                                .mt_1()
                                .p_2()
                                .rounded(cx.theme().radius)
                                .border_1()
                                .border_color(cx.theme().yellow)
                                .text_xs()
                                .text_color(cx.theme().yellow)
                                .child(format!(
                                    "PARKED LOCK: {cls} parked {} (age {}) · {}",
                                    duration_ms(p.parked_duration_ms),
                                    duration_ms(p.age_ms),
                                    short_id(&p.operation_id)
                                ))
                        }))
                    })
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
                                let mut badge = if lease.parked { "PARKED" } else { "LEASE" }.to_string();
                                if let Some(peak) = lease.predicted_peak_mb.filter(|&p| p > 0) {
                                    badge.push_str(&format!(" ~{}", mb(peak as f64)));
                                }
                                rows.push(work_row(&badge, &lease.class_name, lease.weight, lease.pid, lease.op_id(), active, sel, cx));
                            }
                            for waiter in work.waiters {
                                let sel = WorkSelection::Waiter {
                                    operation_id: waiter.operation_id.clone(),
                                    class_name: waiter.class_name.clone(),
                                    weight: waiter.weight,
                                    pid: waiter.pid,
                                };
                                let active = self.work_selection.as_ref().map(|s| s.operation_id()) == Some(sel.operation_id());
                                let mut badge = if waiter.pressure_parked {
                                    "P-PARK".to_string()
                                } else if waiter.start_expired {
                                    "EXPIRED".to_string()
                                } else {
                                    "WAIT".to_string()
                                };
                                if let Some(peak) = waiter.predicted_peak_mb.filter(|&p| p > 0) {
                                    badge.push_str(&format!(" ~{}", mb(peak as f64)));
                                }
                                rows.push(work_row(&badge, &waiter.class_name, waiter.weight, waiter.pid, &waiter.operation_id, active, sel, cx));
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
                            .when(selected_waiter_pressure_reserved, |d| {
                                let label = if selected_waiter.as_ref().map(|w| w.pressure_parked).unwrap_or(false) {
                                    "Parked on host pressure (Run now cannot start it sooner)"
                                } else {
                                    "Reserved on host pressure (waiting for host pressure to clear)"
                                };
                                d.child(div().text_xs().text_color(cx.theme().yellow).child(label))
                            })
                            .child(
                                h_flex()
                                    .gap_2()
                                    .when(
                                        self.work_selection.as_ref().map(|s| s.is_waiter()).unwrap_or(false)
                                            && !selected_waiter_pressure_reserved,
                                        |row| {
                                            row.child(
                                                Button::new("run-now")
                                                    .small()
                                                    .primary()
                                                    .label("Run now")
                                                    .on_click(cx.listener(|this, _, _, cx| this.run_command(CommandKind::RunNow, cx))),
                                            )
                                        },
                                    )
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
                let thresholds = policy.thresholds.clone();
                let work_limits = policy.work_limits.clone();
                let launch = policy.launch_admission.clone();
                this.child(kv("Enabled", if policy.enabled { "yes" } else { "no" }))
                    .child(kv("Admission", if policy.enforce_admission { "enforced" } else { "observe" }))
                    .child(kv("Auto-shed", if policy.auto_shed_critical { "on" } else { "off" }))
                    .child(kv("Profile", policy.profile.as_deref().unwrap_or("—")))
                    .when_some(work_limits, |d, wl| {
                        let mut lines = Vec::new();
                        if let Some(adm) = wl.admission {
                            lines.push(format!("work admission: {adm}"));
                        }
                        if let Some(mem_adm) = wl.memory_admission {
                            lines.push(format!("memory admission: {mem_adm}"));
                        }
                        if let Some(cap) = wl.capacity {
                            if cap >= pressure_core::RESOURCE_ADMISSION_CAPACITY_SENTINEL {
                                lines.push("capacity: resource-headroom mode".into());
                            } else {
                                lines.push(format!("capacity: {cap}"));
                            }
                        }
                        d.when(!lines.is_empty(), |x| x.child(card("Work admission", lines, cx)))
                    })
                    .when_some(thresholds, |d, t| {
                        let lines = vec![
                            format!(
                                "Free memory: warn <= {}% · red <= {}% · crit <= {}%",
                                t.free_warning_percent.unwrap_or(25),
                                t.free_red_percent.unwrap_or(15),
                                t.free_critical_percent.unwrap_or(8)
                            ),
                            format!(
                                "Host CPU: warn >= {:.0}% · red >= {:.0}%",
                                t.host_cpu_warning_percent.unwrap_or(85.0),
                                t.host_cpu_red_percent.unwrap_or(95.0)
                            ),
                            format!(
                                "Agent RSS sum: warn >= {} · red >= {} · crit >= {}",
                                mb(t.agent_total_warning_mb.unwrap_or(8192.0)),
                                mb(t.agent_total_red_mb.unwrap_or(11264.0)),
                                mb(t.agent_total_critical_mb.unwrap_or(13312.0))
                            ),
                            format!(
                                "Swap used: warn >= {} · red >= {}",
                                mb(t.swap_warning_mb.unwrap_or(4096.0)),
                                mb(t.swap_red_mb.unwrap_or(8192.0))
                            ),
                        ];
                        d.child(card("Policy thresholds", lines, cx))
                    })
                    .when_some(launch, |d, la| {
                        let lines = vec![format!(
                            "mode: {} · block depth >= {} · oldest wait >= {}s",
                            la.mode.as_deref().unwrap_or("—"),
                            la.queue_depth_block.unwrap_or(0),
                            la.oldest_wait_block_seconds.unwrap_or(0)
                        )];
                        d.child(card("Launch admission", lines, cx))
                    })
            })
            .when_some(self.board.policy_path.clone(), |this, path| this.child(kv("Path", &path)))
            .when_some(self.board.health.clone(), |this, health| {
                this.child(kv("Daily-driver", if health.daily_driver_ready { "ready" } else { "not ready" }))
                    .children(health.operator_reasons.into_iter().take(4).map(|r| {
                        div().text_xs().text_color(cx.theme().muted_foreground).child(r)
                    }))
            })
    }

    fn render_monitor(&self, cx: &mut Context<Self>) -> impl IntoElement {
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
            .when(self.board.has_recovery_hint || self.board.recovery_hint.is_some(), |this| {
                this.child(
                    Button::new("clear-recovery-hint")
                        .small()
                        .primary()
                        .label("Clear recovery hint")
                        .on_click(cx.listener(|this, _, _, cx| {
                            this.confirm = Some(ConfirmKind::ClearRecoveryHint);
                            cx.notify();
                        })),
                )
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

fn metric_leveled(
    title: &'static str,
    value: String,
    level: PressureLevel,
    cx: &Context<PressureApp>,
) -> impl IntoElement {
    let accent = match level {
        PressureLevel::Critical | PressureLevel::Red => cx.theme().red,
        PressureLevel::Warning => cx.theme().yellow,
        PressureLevel::Normal | PressureLevel::Unknown => cx.theme().foreground,
    };
    v_flex()
        .flex_1()
        .p_3()
        .gap_1()
        .rounded(cx.theme().radius)
        .border_1()
        .border_color(if level.rank() >= PressureLevel::Warning.rank() {
            accent
        } else {
            cx.theme().border
        })
        .child(div().text_xs().text_color(cx.theme().muted_foreground).child(title))
        .child(div().text_lg().font_semibold().text_color(accent).child(value))
}

fn metric(title: &'static str, value: String, cx: &Context<PressureApp>) -> impl IntoElement {
    metric_leveled(title, value, PressureLevel::Normal, cx)
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
    kind: &str,
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
