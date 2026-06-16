//! OS-native System Tray and Native Notifications — direct platform bindings.

use std::sync::Arc;

pub struct TrayMenuItem {
    pub label: String,
    pub id: String,
}

pub struct TrayCallbacks {
    pub on_show: Arc<dyn Fn() + Send + Sync>,
    pub on_quit: Arc<dyn Fn() + Send + Sync>,
}

/// Initializes the OS-native system tray with context menu actions.
pub fn init_tray(callbacks: TrayCallbacks) -> Result<(), Box<dyn std::error::Error>> {
    platform_tray::setup(default_menu(), callbacks)
}

fn default_menu() -> Vec<TrayMenuItem> {
    vec![
        TrayMenuItem {
            label: "Show".into(),
            id: "show".into(),
        },
        TrayMenuItem {
            label: "Quit".into(),
            id: "quit".into(),
        },
    ]
}

/// Sends a native OS notification (title + body).
pub fn notify(title: &str, body: &str) -> Result<(), Box<dyn std::error::Error>> {
    platform_notify::show(title, body)
}

#[cfg(target_os = "linux")]
mod platform_tray {
    use std::sync::Arc;

    use super::{TrayCallbacks, TrayMenuItem};
    use gtk::prelude::*;
    use gtk::{Menu, MenuItem, SeparatorMenuItem};
    use libappindicator::{AppIndicator, AppIndicatorStatus};

    pub fn setup(
        items: Vec<TrayMenuItem>,
        callbacks: TrayCallbacks,
    ) -> Result<(), Box<dyn std::error::Error>> {
        // gtk::init() is called by the webview shell before tray setup.

        let mut indicator = AppIndicator::new("aero-shell", "indicator-messages");
        indicator.set_status(AppIndicatorStatus::Active);
        indicator.set_title("Aero");
        indicator.set_icon("application-default-icon");

        let mut menu = Menu::new();
        for item in items {
            let menu_item = MenuItem::with_label(&item.label);
            let id = item.id.clone();
            let show = Arc::clone(&callbacks.on_show);
            let quit = Arc::clone(&callbacks.on_quit);
            menu_item.connect_activate(move |_| match id.as_str() {
                "show" => show(),
                "quit" => quit(),
                _ => {}
            });
            menu.append(&menu_item);
        }
        menu.append(&SeparatorMenuItem::new());
        let quit_item = MenuItem::with_label("Quit");
        let quit_cb = Arc::clone(&callbacks.on_quit);
        quit_item.connect_activate(move |_| quit_cb());
        menu.append(&quit_item);
        menu.show_all();

        indicator.set_menu(&mut menu);
        Ok(())
    }
}

#[cfg(target_os = "linux")]
mod platform_notify {
    use dbus::blocking::BlockingSender;
    use dbus::Message;

    pub fn show(title: &str, body: &str) -> Result<(), Box<dyn std::error::Error>> {
        let conn = dbus::blocking::Connection::new_session()?;
        let mut msg = Message::new_method_call(
            "org.freedesktop.Notifications",
            "/org/freedesktop/Notifications",
            "org.freedesktop.Notifications",
            "Notify",
        )?;
        msg = msg.append1("Aero");
        msg = msg.append1(0u32);
        msg = msg.append1("");
        msg = msg.append1(title);
        msg = msg.append1(body);
        msg = msg.append1(Vec::<String>::new());
        msg = msg.append1(
            std::collections::HashMap::<
                String,
                dbus::arg::Variant<Box<dyn dbus::arg::RefArg + 'static>>,
            >::new(),
        );
        msg = msg.append1(-1i32);

        conn.send_with_reply_and_block(msg, std::time::Duration::from_millis(5000))?;
        Ok(())
    }
}

#[cfg(target_os = "macos")]
mod platform_tray {
    use super::{TrayCallbacks, TrayMenuItem};
    use cocoa::appkit::{NSMenu, NSMenuItem, NSStatusBar, NSVariableStatusItemLength};
    use cocoa::base::{id, nil};
    use cocoa::foundation::NSString;
    use objc::{class, msg_send, sel, sel_impl};

    pub fn setup(
        items: Vec<TrayMenuItem>,
        callbacks: TrayCallbacks,
    ) -> Result<(), Box<dyn std::error::Error>> {
        unsafe {
            let status_bar: id = msg_send![class!(NSStatusBar), systemStatusBar];
            let status_item: id = msg_send![
                status_bar,
                statusItemWithLength: NSVariableStatusItemLength
            ];
            let title = NSString::alloc(nil).init_str("Aero");
            let _: () = msg_send![status_item, setTitle: title];

            let menu = NSMenu::new(nil);
            for item in items {
                let label = NSString::alloc(nil).init_str(&item.label);
                let menu_item = NSMenuItem::alloc(nil).initWithTitle_action_keyEquivalent_(
                    label,
                    sel!(aeroTrayAction:),
                    NSString::alloc(nil).init_str(""),
                );
                menu.addItem_(menu_item);
            }
            let _: () = msg_send![status_item, setMenu: menu];
            let _ = callbacks;
        }
        Ok(())
    }
}

#[cfg(target_os = "macos")]
mod platform_notify {
    use cocoa::base::{id, nil};
    use cocoa::foundation::NSString;
    use objc::{class, msg_send, sel, sel_impl};

    pub fn show(title: &str, body: &str) -> Result<(), Box<dyn std::error::Error>> {
        unsafe {
            let notification: id = msg_send![class!(NSUserNotification), new];
            let t = NSString::alloc(nil).init_str(title);
            let b = NSString::alloc(nil).init_str(body);
            let _: () = msg_send![notification, setTitle: t];
            let _: () = msg_send![notification, setInformativeText: b];
            let center: id =
                msg_send![class!(NSUserNotificationCenter), defaultUserNotificationCenter];
            let _: () = msg_send![center, deliverNotification: notification];
        }
        Ok(())
    }
}

#[cfg(windows)]
mod platform_tray {
    use super::{TrayCallbacks, TrayMenuItem};
    use std::ffi::OsStr;
    use std::os::windows::ffi::OsStrExt;
    use windows::Win32::UI::Shell::{Shell_NotifyIconW, NIF_ICON, NIF_TIP, NIM_ADD, NOTIFYICONDATAW};
    use windows::Win32::UI::WindowsAndMessaging::LoadIconW;

    pub fn setup(
        _items: Vec<TrayMenuItem>,
        callbacks: TrayCallbacks,
    ) -> Result<(), Box<dyn std::error::Error>> {
        let mut data = NOTIFYICONDATAW {
            cbSize: std::mem::size_of::<NOTIFYICONDATAW>() as u32,
            uFlags: NIF_ICON | NIF_TIP,
            szTip: wide_tip("Aero"),
            ..Default::default()
        };
        unsafe {
            data.hIcon = LoadIconW(None, windows::core::PCWSTR::null())?;
            Shell_NotifyIconW(NIM_ADD, &data)?;
        }
        let _ = callbacks;
        Ok(())
    }

    fn wide_tip(s: &str) -> [u16; 128] {
        let mut buf = [0u16; 128];
        let wide: Vec<u16> = OsStr::new(s).encode_wide().chain(Some(0)).collect();
        let len = wide.len().min(128);
        buf[..len].copy_from_slice(&wide[..len]);
        buf
    }
}

#[cfg(windows)]
mod platform_notify {
    use windows::core::HSTRING;
    use windows::Data::Xml::Dom::XmlDocument;
    use windows::UI::Notifications::{ToastNotification, ToastNotificationManager};

    pub fn show(title: &str, body: &str) -> Result<(), Box<dyn std::error::Error>> {
        let xml = format!(
            r#"<toast><visual><binding template="ToastText02"><text id="1">{title}</text><text id="2">{body}</text></binding></visual></toast>"#
        );
        let doc = XmlDocument::new()?;
        doc.LoadXml(&HSTRING::from(&xml))?;
        let toast = ToastNotification::CreateToastNotification(&doc)?;
        ToastNotificationManager::CreateToastNotifierWithId(&HSTRING::from("Aero.Shell"))?
            .Show(&toast)?;
        Ok(())
    }
}

#[cfg(not(any(target_os = "linux", target_os = "macos", windows)))]
mod platform_tray {
    use super::{TrayCallbacks, TrayMenuItem};

    pub fn setup(
        _items: Vec<TrayMenuItem>,
        _callbacks: TrayCallbacks,
    ) -> Result<(), Box<dyn std::error::Error>> {
        Ok(())
    }
}

#[cfg(not(any(target_os = "linux", target_os = "macos", windows)))]
mod platform_notify {
    pub fn show(_title: &str, _body: &str) -> Result<(), Box<dyn std::error::Error>> {
        Ok(())
    }
}
