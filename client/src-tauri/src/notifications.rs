use std::sync::Arc;
/// Call platform APIs directly with fixed, content-free messages. No shell or window is required.
#[cfg(target_os = "macos")]
pub fn notify(body: &'static str, failure: Arc<dyn Fn() + Send + Sync>) {
    use objc2_foundation::NSString;
    use objc2_user_notifications::{
        UNAuthorizationOptions, UNMutableNotificationContent, UNNotificationRequest,
        UNUserNotificationCenter,
    };
    let center = UNUserNotificationCenter::currentNotificationCenter();
    let callback = block2::RcBlock::new(
        move |granted: objc2::runtime::Bool, error: *mut objc2_foundation::NSError| {
            if !granted.as_bool() || !error.is_null() {
                failure();
                return;
            }
            let content = UNMutableNotificationContent::new();
            content.setTitle(&NSString::from_str("LinkNestX"));
            content.setBody(&NSString::from_str(body));
            let request = UNNotificationRequest::requestWithIdentifier_content_trigger(
                &NSString::from_str(&uuid::Uuid::new_v4().to_string()),
                &content,
                None,
            );
            let failure = failure.clone();
            let completed = block2::RcBlock::new(move |error: *mut objc2_foundation::NSError| {
                if !error.is_null() {
                    failure();
                }
            });
            UNUserNotificationCenter::currentNotificationCenter()
                .addNotificationRequest_withCompletionHandler(&request, Some(&completed));
        },
    );
    center.requestAuthorizationWithOptions_completionHandler(
        UNAuthorizationOptions::Alert | UNAuthorizationOptions::Sound,
        &callback,
    );
}
#[cfg(target_os = "windows")]
pub fn notify(body: &'static str, failure: Arc<dyn Fn() + Send + Sync>) {
    use windows::{
        core::HSTRING,
        Data::Xml::Dom::XmlDocument,
        UI::Notifications::{ToastNotification, ToastNotificationManager},
    };
    let result = (|| -> windows::core::Result<()> {
        let xml = XmlDocument::new()?;
        xml.LoadXml(&HSTRING::from(format!("<toast><visual><binding template=\"ToastGeneric\"><text>LinkNestX</text><text>{body}</text></binding></visual></toast>")))?;
        let toast = ToastNotification::CreateToastNotification(&xml)?;
        ToastNotificationManager::CreateToastNotifierWithId(&HSTRING::from("com.linknestx.client"))?
            .Show(&toast)
    })();
    if result.is_err() {
        failure()
    }
}
#[cfg(not(any(target_os = "macos", target_os = "windows")))]
pub fn notify(_body: &'static str, failure: Arc<dyn Fn() + Send + Sync>) {
    failure()
}
