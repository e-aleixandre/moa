import Foundation
import WebKit

// window.moaSavedSecrets: optional "Save on this iPhone" for the password a
// moa action asks the owner for. Only the paired server's main frame may call
// it, and only `use` ever returns a value (after the Keychain's own Face ID).
final class SavedSecretsMessageHandler: NSObject, WKScriptMessageHandler {
    static let name = "moaSavedSecrets"
    static let javaScript = #"""
    (() => {
      if (window.moaSavedSecrets) return;
      let nextId = 1;
      const calls = new Map();
      const call = (method, options = {}) => new Promise((resolve, reject) => {
        const id = String(nextId++);
        calls.set(id, { resolve, reject });
        window.webkit.messageHandlers.moaSavedSecrets.postMessage({ id, method, options });
      });
      window.moaSavedSecrets = {
        status: (o) => call("status", o),
        save: (o) => call("save", o),
        use: (o) => call("use", o),
        list: () => call("list"),
        forget: (o) => call("forget", o),
        forgetAll: () => call("forgetAll"),
        touch: (o) => call("touch", o),
        __receive(message) {
          const pending = calls.get(message.id);
          if (!pending) return;
          calls.delete(message.id);
          if (message.ok) {
            pending.resolve(message.value);
            return;
          }
          const error = new Error(message.error || "Saved password request failed.");
          error.code = message.code || "unavailable";
          pending.reject(error);
        }
      };
    })();
    """#

    weak var webView: WKWebView?

    private let store: SavedSecretsStore
    private let queue = DispatchQueue(label: "com.ealeixandre.moa.saved-secrets", qos: .userInitiated)

    init(store: SavedSecretsStore = SavedSecretsStore()) {
        self.store = store
    }

    func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
        guard
            message.frameInfo.isMainFrame,
            let body = message.body as? [String: Any],
            let id = body["id"] as? String,
            let method = body["method"] as? String
        else { return }
        let options = body["options"] as? [String: Any] ?? [:]

        guard
            NativeServerBinding.matches(message.frameInfo.securityOrigin),
            let serverID = NativeServerBinding.origin
        else {
            respond(id: id, error: .notAuthorized)
            return
        }

        let fence = ReplyFence(generation: DocumentGeneration.shared.current, origin: serverID)

        // Keychain calls block (use shows Face ID), so they never run on main.
        queue.async { [weak self] in
            guard let self else { return }
            // Queued behind a navigation or an unpair: do not even start.
            guard fence.generation == DocumentGeneration.shared.current else { return }
            do {
                let value = try self.handle(method: method, options: options, serverID: serverID, fence: fence)
                self.respond(id: id, value: value, fence: fence)
            } catch {
                self.respond(id: id, error: Self.bridgeError(error), fence: fence)
            }
        }
    }

    private func handle(method: String, options: [String: Any], serverID: String, fence: ReplyFence) throws -> Any {
        typealias L = SavedSecretLimits
        switch method {
        case "status":
            return try store.status(
                serverID: serverID,
                alias: try alias(options),
                fingerprint: try fingerprint(options)
            )
        case "save":
            guard
                let label = SavedSecretInput.text(options["label"], max: L.maxLabel),
                let summary = SavedSecretInput.text(options["actionSummary"], max: L.maxSummary, allowEmpty: true),
                var value = SavedSecretInput.secretValue(options["value"])
            else { throw BridgeError.invalid }
            try store.save(
                serverID: serverID, alias: try alias(options), label: label,
                summary: summary, fingerprint: try fingerprint(options), value: &value,
                stillValid: { fence.generation == DocumentGeneration.shared.current }
            )
            return ["ok": true]
        case "use":
            let reason = SavedSecretInput.text(options["reason"], max: L.maxReason) ?? "Use your saved password"
            var data = try store.use(serverID: serverID, alias: try alias(options), reason: reason)
            defer { data.resetBytes(in: 0..<data.count) }
            guard let value = String(data: data, encoding: .utf8) else { throw SavedSecretsError.invalidData }
            return ["value": value]
        case "list":
            return try store.list(serverID: serverID).map(\.listEntry)
        case "forget":
            try store.forget(serverID: serverID, alias: try alias(options))
            return [:]
        case "forgetAll":
            try store.forgetAll(serverID: serverID)
            return [:]
        case "touch":
            try store.touch(serverID: serverID, alias: try alias(options), fingerprint: try fingerprint(options))
            return [:]
        default:
            throw BridgeError.unknownMethod
        }
    }

    private func alias(_ options: [String: Any]) throws -> String {
        guard let value = SavedSecretInput.text(options["alias"], max: SavedSecretLimits.maxAlias) else {
            throw BridgeError.invalid
        }
        return value
    }

    private func fingerprint(_ options: [String: Any]) throws -> String {
        guard let value = SavedSecretInput.text(options["actionFingerprint"], max: SavedSecretLimits.maxFingerprint) else {
            throw BridgeError.invalid
        }
        return value
    }

    private enum BridgeError: Error {
        case invalid
        case notAuthorized
        case unknownMethod
    }

    private struct Failure {
        let code: String
        let message: String
    }

    private static func bridgeError(_ error: Error) -> Failure {
        switch error {
        case BridgeError.invalid: return Failure(code: "invalid", message: "The saved password request is invalid.")
        case BridgeError.notAuthorized: return Failure(code: "not_authorized", message: "This page cannot use saved passwords.")
        case BridgeError.unknownMethod: return Failure(code: "unsupported", message: "The saved password request is not supported.")
        case SavedSecretsError.notFound: return Failure(code: "not_found", message: "No saved password for this action.")
        case SavedSecretsError.cancelled: return Failure(code: "cancelled", message: "Face ID was cancelled.")
        case SavedSecretsError.authFailed: return Failure(code: "auth_failed", message: "Face ID did not recognise you.")
        case SavedSecretsError.biometryUnavailable:
            return Failure(code: "biometry_unavailable", message: "Face ID and a device passcode are required to save a password.")
        default: return Failure(code: "keychain", message: "The saved password could not be accessed.")
        }
    }

    private func respond(id: String, value: Any, fence: ReplyFence? = nil) {
        send(["id": id, "ok": true, "value": value], fence: fence)
    }

    private func respond(id: String, error: BridgeError) {
        respond(id: id, error: Self.bridgeError(error), fence: nil)
    }

    private func respond(id: String, error: Failure, fence: ReplyFence?) {
        send(["id": id, "ok": false, "error": error.message, "code": error.code], fence: fence)
    }

    // `fence` nil is only for refusals produced at admission, which carry no value.
    private func send(_ message: [String: Any], fence: ReplyFence?) {
        guard
            JSONSerialization.isValidJSONObject(message),
            let data = try? JSONSerialization.data(withJSONObject: message),
            let json = String(data: data, encoding: .utf8)
        else { return }
        DispatchQueue.main.async { [weak self] in
            guard let webView = self?.webView else { return }
            if let fence {
                let onPairedOrigin = webView.url.map(NativeServerBinding.matches) == true
                guard fence.permits(
                    generation: DocumentGeneration.shared.current,
                    boundOrigin: NativeServerBinding.origin,
                    documentMatchesPairing: onPairedOrigin
                ) else { return }
            }
            webView.evaluateJavaScript("window.moaSavedSecrets?.__receive(\(json));")
        }
    }
}
