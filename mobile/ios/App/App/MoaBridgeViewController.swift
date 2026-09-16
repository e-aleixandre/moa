import Capacitor
import Foundation
import WebKit

final class MoaBridgeViewController: CAPBridgeViewController {
    private let shareInboxHandler = ShareInboxMessageHandler()
    private let deviceAuthHandler = DeviceAuthMessageHandler()

    override func webView(with frame: CGRect, configuration: WKWebViewConfiguration) -> WKWebView {
        configuration.userContentController.addUserScript(WKUserScript(
            source: ShareInboxMessageHandler.javaScript,
            injectionTime: .atDocumentStart,
            forMainFrameOnly: true
        ))
        configuration.userContentController.addUserScript(WKUserScript(
            source: DeviceAuthMessageHandler.javaScript,
            injectionTime: .atDocumentStart,
            forMainFrameOnly: true
        ))
        configuration.userContentController.add(shareInboxHandler, name: ShareInboxMessageHandler.name)
        configuration.userContentController.add(deviceAuthHandler, name: DeviceAuthMessageHandler.name)
        return super.webView(with: frame, configuration: configuration)
    }

    override func capacitorDidLoad() {
        bridge?.registerPluginInstance(PairedServerNavigationPlugin())
        shareInboxHandler.webView = webView
        deviceAuthHandler.webView = webView
    }
}

// Capacitor's static allowNavigation list is host-only and the server is not
// known when this app is built. A plugin policy runs before Capacitor opens an
// outside URL in Safari, so it can admit the one full HTTPS origin paired at
// runtime without admitting another port, scheme, or host. Returning nil for
// everything else preserves Capacitor's normal external-link handling.
final class PairedServerNavigationPlugin: CAPInstancePlugin, CAPBridgedPlugin {
    let identifier = "MoaPairedServerNavigation"
    let jsName = "MoaPairedServerNavigation"
    let pluginMethods: [CAPPluginMethod] = []

    override func shouldOverrideLoad(_ navigationAction: WKNavigationAction) -> NSNumber? {
        guard
            let url = navigationAction.request.url,
            NativeServerBinding.matches(url)
        else { return nil }
        return NSNumber(value: false)
    }
}

private final class ShareInboxMessageHandler: NSObject, WKScriptMessageHandler {
    static let name = "moaShareInbox"
    static let javaScript = #"""
    (() => {
      if (window.MoaShareInbox) return;
      let nextId = 1;
      const calls = new Map();
      const call = (method, options = {}) => new Promise((resolve, reject) => {
        const id = String(nextId++);
        calls.set(id, { resolve, reject });
        window.webkit.messageHandlers.moaShareInbox.postMessage({ id, method, options });
      });
      window.MoaShareInbox = {
        status: () => call("status"),
        bindServer: (origin) => call("bindServer", { origin }),
        clearServer: () => call("clearServer"),
        peek: () => call("peek"),
        acknowledge: (id) => call("acknowledge", { id }),
        __receive(message) {
          const pending = calls.get(message.id);
          if (!pending) return;
          calls.delete(message.id);
          if (message.ok) pending.resolve(message.value);
          else pending.reject(new Error(message.error || "Native share failed."));
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

        do {
            switch method {
            case "status":
                guard isLocal(message.frameInfo.securityOrigin) else { throw BridgeError.notAuthorized }
                respond(id: id, value: ["pending": try SharedInbox.pendingCount()])
            case "bindServer":
                guard isLocal(message.frameInfo.securityOrigin) else { throw BridgeError.notAuthorized }
                let origin = try validatedOrigin(options["origin"] as? String)
                NativeServerBinding.bind(origin)
                respond(id: id, value: [:])
            case "clearServer":
                guard isLocal(message.frameInfo.securityOrigin) else { throw BridgeError.notAuthorized }
                NativeServerBinding.clear()
                respond(id: id, value: [:])
            case "peek":
                try requireBoundServer(message.frameInfo.securityOrigin)
                // `??` needs both sides to be the same type, and a share is a
                // dictionary while the empty case has to reach JSON as null.
                // Widening to Any is what lets one expression carry both.
                let share: Any = try nextShare() ?? NSNull()
                respond(id: id, value: ["share": share])
            case "acknowledge":
                try requireBoundServer(message.frameInfo.securityOrigin)
                guard let shareID = options["id"] as? String else { throw SharedInboxError.corrupt }
                try SharedInbox.acknowledge(id: shareID)
                respond(id: id, value: [:])
            default:
                throw BridgeError.unknownMethod
            }
        } catch {
            respond(id: id, error: error.localizedDescription)
        }
    }

    private func nextShare() throws -> [String: Any]? {
        guard let envelope = try SharedInbox.next() else { return nil }
        var totalBytes = 0
        let files: [[String: Any]] = try envelope.manifest.files.map { file in
            guard UUID(uuidString: file.storedName) != nil else { throw SharedInboxError.corrupt }
            let source = envelope.directory.appendingPathComponent(file.storedName)
            let data = try Data(contentsOf: source, options: [.mappedIfSafe])
            totalBytes += data.count
            guard
                data.count == file.size,
                totalBytes <= SharedInbox.maximumBytes
            else { throw SharedInboxError.corrupt }
            return [
                "name": file.name,
                "mime": file.mime,
                "size": file.size,
                "data": data.base64EncodedString()
            ]
        }
        return [
            "id": envelope.manifest.id,
            "title": envelope.manifest.title,
            "text": envelope.manifest.text,
            "url": envelope.manifest.url,
            "files": files
        ]
    }

    private func requireBoundServer(_ securityOrigin: WKSecurityOrigin) throws {
        guard NativeServerBinding.matches(securityOrigin) else { throw BridgeError.notAuthorized }
    }

    private func validatedOrigin(_ raw: String?) throws -> String {
        guard
            let raw,
            let components = URLComponents(string: raw),
            components.scheme == "https",
            components.host != nil,
            components.path.isEmpty || components.path == "/",
            components.query == nil,
            components.fragment == nil,
            let origin = components.url?.absoluteString.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
        else { throw BridgeError.invalidOrigin }
        return origin
    }

    private func isLocal(_ origin: WKSecurityOrigin) -> Bool {
        origin.protocol == "capacitor" && origin.host == "localhost"
    }

    private func respond(id: String, value: Any) {
        send(["id": id, "ok": true, "value": value])
    }

    private func respond(id: String, error: String) {
        send(["id": id, "ok": false, "error": error])
    }

    private func send(_ message: [String: Any]) {
        guard
            JSONSerialization.isValidJSONObject(message),
            let data = try? JSONSerialization.data(withJSONObject: message),
            let json = String(data: data, encoding: .utf8)
        else { return }
        DispatchQueue.main.async { [weak self] in
            self?.webView?.evaluateJavaScript("window.MoaShareInbox?.__receive(\(json));")
        }
    }

    private enum BridgeError: LocalizedError {
        case invalidOrigin
        case notAuthorized
        case unknownMethod

        var errorDescription: String? {
            switch self {
            case .invalidOrigin: return "The paired server address is invalid."
            case .notAuthorized: return "This page cannot read moa's shared items."
            case .unknownMethod: return "The native share request is not supported."
            }
        }
    }
}
