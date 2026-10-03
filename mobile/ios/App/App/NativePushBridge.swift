import Foundation
import WebKit

// `window.MoaNativePush` for the paired frontend's notifications switch.
// Only the main frame of the paired origin may call it. It accepts no keys,
// URLs, destinations or credentials and returns none: status is permission,
// enabled/registered and the server's last delivery result.
final class NativePushMessageHandler: NSObject, WKScriptMessageHandler {
    static let name = "moaNativePush"
    static let javaScript = #"""
    (() => {
      if (window.MoaNativePush) return;
      let nextId = 1;
      const calls = new Map();
      const call = (method, options = {}) => new Promise((resolve, reject) => {
        const id = String(nextId++);
        calls.set(id, { resolve, reject });
        window.webkit.messageHandlers.moaNativePush.postMessage({ id, method, options });
      });
      window.MoaNativePush = {
        status: () => call("status"),
        enable: () => call("enable"),
        disable: () => call("disable"),
        setVisibleSession: (session) => call("setVisibleSession", {
          session: typeof session === "string" ? session : null
        }),
        __receive(message) {
          const pending = calls.get(message.id);
          if (!pending) return;
          calls.delete(message.id);
          if (message.ok) {
            pending.resolve(message.value);
            return;
          }
          const error = new Error("Native notifications failed.");
          error.code = message.code || "unavailable";
          pending.reject(error);
        }
      };
    })();
    """#

    weak var webView: WKWebView?

    func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
        guard
            message.frameInfo.isMainFrame,
            let body = message.body as? [String: Any],
            let id = body["id"] as? String,
            let method = body["method"] as? String,
            let options = body["options"] as? [String: Any]
        else { return }
        guard NativeServerBinding.matches(message.frameInfo.securityOrigin) else {
            respond(id: id, code: "not_authorized")
            return
        }

        Task { @MainActor in
            let registration = NativePushRegistration.shared
            switch method {
            case "status":
                self.respond(id: id, value: await registration.status())
            case "enable":
                do {
                    try await registration.enable()
                    self.respond(id: id, value: await registration.status())
                } catch {
                    self.respond(id: id, code: (error as? NativePushError)?.code ?? "unavailable")
                }
            case "disable":
                do {
                    try await registration.disable()
                    self.respond(id: id, value: await registration.status())
                } catch {
                    self.respond(id: id, code: (error as? NativePushError)?.code ?? "unavailable")
                }
            case "setVisibleSession":
                PushNavigator.shared.setVisibleSession(options["session"] as? String)
                self.respond(id: id, value: [:])
            default:
                self.respond(id: id, code: "unsupported")
            }
        }
    }

    private func respond(id: String, value: Any) {
        send(["id": id, "ok": true, "value": value])
    }

    private func respond(id: String, code: String) {
        send(["id": id, "ok": false, "code": code])
    }

    private func send(_ message: [String: Any]) {
        guard
            JSONSerialization.isValidJSONObject(message),
            let data = try? JSONSerialization.data(withJSONObject: message),
            let json = String(data: data, encoding: .utf8)
        else { return }
        DispatchQueue.main.async { [weak self] in
            self?.webView?.evaluateJavaScript("window.MoaNativePush?.__receive(\(json));")
        }
    }
}
