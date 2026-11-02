mod app;

use gpui::*;
use gpui_component::{Root, Theme, ThemeMode, TitleBar};
use gpui_component_assets::Assets;

use crate::app::{Go1, Go2, Go3, Go4, Go5, Go6, Go7, Go8, LiveSample, Palette, PressureApp, Quit, Refresh};

fn main() {
    let app = gpui_platform::application().with_assets(Assets);
    app.run(move |cx| {
        gpui_component::init(cx);

        cx.bind_keys([
            #[cfg(target_os = "macos")]
            KeyBinding::new("cmd-q", Quit, None),
            #[cfg(target_os = "macos")]
            KeyBinding::new("cmd-r", Refresh, None),
            #[cfg(target_os = "macos")]
            KeyBinding::new("cmd-shift-r", LiveSample, None),
            #[cfg(target_os = "macos")]
            KeyBinding::new("cmd-k", Palette, None),
            #[cfg(target_os = "macos")]
            KeyBinding::new("cmd-1", Go1, None),
            #[cfg(target_os = "macos")]
            KeyBinding::new("cmd-2", Go2, None),
            #[cfg(target_os = "macos")]
            KeyBinding::new("cmd-3", Go3, None),
            #[cfg(target_os = "macos")]
            KeyBinding::new("cmd-4", Go4, None),
            #[cfg(target_os = "macos")]
            KeyBinding::new("cmd-5", Go5, None),
            #[cfg(target_os = "macos")]
            KeyBinding::new("cmd-6", Go6, None),
            #[cfg(target_os = "macos")]
            KeyBinding::new("cmd-7", Go7, None),
            #[cfg(target_os = "macos")]
            KeyBinding::new("cmd-8", Go8, None),
            #[cfg(not(target_os = "macos"))]
            KeyBinding::new("alt-f4", Quit, None),
            #[cfg(not(target_os = "macos"))]
            KeyBinding::new("ctrl-r", Refresh, None),
            #[cfg(not(target_os = "macos"))]
            KeyBinding::new("ctrl-shift-r", LiveSample, None),
            #[cfg(not(target_os = "macos"))]
            KeyBinding::new("ctrl-k", Palette, None),
        ]);

        cx.on_action(|_: &Quit, cx: &mut App| {
            cx.quit();
        });

        let window_options = WindowOptions {
            window_bounds: Some(WindowBounds::centered(size(px(1280.), px(820.)), cx)),
            ..TitleBar::window_options()
        };

        cx.activate(true);

        cx.spawn(async move |cx| {
            cx.open_window(window_options, |window, cx| {
                window.activate_window();
                window.set_window_title("Session Pressure");
                Theme::change(ThemeMode::Dark, Some(window), cx);
                let view = cx.new(|cx| PressureApp::new(window, cx));
                cx.new(|cx| Root::new(view, window, cx))
            })
            .expect("Failed to open Session Pressure window");
        })
        .detach();
    });
}
