import Foundation
import UserNotifications

// Replaces the relay's generic alert with the decrypted content. It reads only
// the push secret S (push-only Keychain group) and never touches the network
// or the device credential. Whatever cannot be authenticated is shown as the
// localized fallback, which sounds: losing a question is worse than an extra
// generic alert.
final class NotificationService: UNNotificationServiceExtension {
    private let lock = NSLock()
    private var contentHandler: ((UNNotificationContent) -> Void)?
    private var fallback: UNNotificationContent?

    override func didReceive(
        _ request: UNNotificationRequest,
        withContentHandler contentHandler: @escaping (UNNotificationContent) -> Void
    ) {
        let envelope = request.content.userInfo["e"]
        let fallback = Self.fallbackContent(envelope: envelope)
        lock.lock()
        self.contentHandler = contentHandler
        self.fallback = fallback
        lock.unlock()

        guard
            let secret = try? PushSecretStore.load(),
            let keys = PushEnvelope.deriveKeys(secret: secret),
            let message = PushEnvelope.open(envelope, keys: keys, now: Date())
        else {
            deliver(fallback)
            return
        }

        // A fresh content object: nothing the relay put next to the envelope
        // (category, attachments, extra keys) survives, only `e` so a tap can
        // re-authenticate it.
        let content = UNMutableNotificationContent()
        content.title = message.title
        content.body = message.body
        content.threadIdentifier = message.thread
        content.userInfo = fallback.userInfo
        switch message.level {
        case .urgent:
            content.interruptionLevel = .timeSensitive
            content.sound = .default
        case .active:
            content.interruptionLevel = .active
            content.sound = .default
        case .passive:
            content.interruptionLevel = .passive
            content.sound = nil
        }
        deliver(content)
    }

    override func serviceExtensionTimeWillExpire() {
        lock.lock()
        let fallback = self.fallback
        lock.unlock()
        if let fallback { deliver(fallback) }
    }

    private func deliver(_ content: UNNotificationContent) {
        lock.lock()
        let handler = contentHandler
        contentHandler = nil
        lock.unlock()
        handler?(content)
    }

    private static func fallbackContent(envelope: Any?) -> UNNotificationContent {
        let content = UNMutableNotificationContent()
        content.title = NSLocalizedString("PUSH_FALLBACK_TITLE", comment: "Title of a notification moa could not decrypt")
        content.body = NSLocalizedString("PUSH_FALLBACK_BODY", comment: "Body of a notification moa could not decrypt")
        content.sound = .default
        content.interruptionLevel = .active
        if let envelope { content.userInfo = ["e": envelope] }
        return content
    }
}
