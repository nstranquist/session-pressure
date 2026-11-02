use crate::models::Section;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum CommandKind {
    GoSection(Section),
    Refresh,
    LiveSample,
    Palette,
    RunNow,
    RunAll,
    ClearPins,
    StopTree,
    Reschedule,
    CopyOp,
    CopyBrief,
    ApplyBalanced,
    ApplyThroughput,
    ApplyInteractive,
    ApplyObserve,
}

#[derive(Debug, Clone)]
pub struct Command {
    pub id: &'static str,
    pub title: String,
    pub hint: &'static str,
    pub kind: CommandKind,
}

pub fn commands() -> Vec<Command> {
    let mut out = vec![
        Command { id: "refresh", title: "Refresh board".into(), hint: "⌘R", kind: CommandKind::Refresh },
        Command { id: "live", title: "Live sample".into(), hint: "⇧⌘R", kind: CommandKind::LiveSample },
        Command { id: "palette", title: "Command palette".into(), hint: "⌘K", kind: CommandKind::Palette },
        Command { id: "run-now", title: "Run now (selected waiter)".into(), hint: "↵", kind: CommandKind::RunNow },
        Command { id: "run-all", title: "Pin whole queue".into(), hint: "⇧↵", kind: CommandKind::RunAll },
        Command { id: "clear-pins", title: "Clear promotion pins".into(), hint: "", kind: CommandKind::ClearPins },
        Command { id: "stop", title: "Stop selected lease tree".into(), hint: "⌫", kind: CommandKind::StopTree },
        Command { id: "reschedule", title: "Reschedule selected job".into(), hint: "", kind: CommandKind::Reschedule },
        Command { id: "copy-op", title: "Copy operation id".into(), hint: "⌘C", kind: CommandKind::CopyOp },
        Command { id: "copy-brief", title: "Copy inspect brief".into(), hint: "", kind: CommandKind::CopyBrief },
        Command { id: "balanced", title: "Apply balanced profile".into(), hint: "", kind: CommandKind::ApplyBalanced },
        Command { id: "throughput", title: "Apply throughput profile".into(), hint: "", kind: CommandKind::ApplyThroughput },
        Command { id: "interactive", title: "Apply interactive profile".into(), hint: "", kind: CommandKind::ApplyInteractive },
        Command { id: "observe", title: "Apply observe profile".into(), hint: "", kind: CommandKind::ApplyObserve },
    ];
    for section in Section::all() {
        out.push(Command {
            id: section.title(),
            title: format!("Go to {}", section.title()),
            hint: match section.shortcut() {
                '1' => "⌘1",
                '2' => "⌘2",
                '3' => "⌘3",
                '4' => "⌘4",
                '5' => "⌘5",
                '6' => "⌘6",
                '7' => "⌘7",
                '8' => "⌘8",
                _ => "",
            },
            kind: CommandKind::GoSection(*section),
        });
    }
    out
}

pub fn filter_commands(query: &str) -> Vec<Command> {
    let q = query.trim().to_lowercase();
    if q.is_empty() {
        return commands();
    }
    commands()
        .into_iter()
        .filter(|c| c.title.to_lowercase().contains(&q) || c.id.to_lowercase().contains(&q))
        .collect()
}
