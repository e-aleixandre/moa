import Capacitor
import Foundation
import Network
import UIKit
import WebKit

final class MoaBridgeViewController: CAPBridgeViewController {
    private let shareInboxHandler = ShareInboxMessageHandler()
    private let deviceAuthHandler = DeviceAuthMessageHandler()
    private var serverRecovery: PairedServerRecoveryController?

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
        if let webView, let capacitorDelegate = webView.navigationDelegate {
            let recovery = PairedServerRecoveryController(webView: webView, forwardingTo: capacitorDelegate)
            serverRecovery = recovery
            webView.navigationDelegate = recovery
        }
    }
}

// Capacitor owns WKWebView's navigation delegate. This proxy adds recovery for
// transport failures while forwarding every other callback to Capacitor, so
// its navigation policy, bridge reset and auth handling stay intact. Capacitor
// remains the separate WKUIDelegate, including for media capture permission.
private final class PairedServerRecoveryController: NSObject, WKNavigationDelegate {
    private static let retryDelays: [TimeInterval] = [1, 2, 4, 8, 15]
    private static let retryableTransportErrors: Set<Int> = [
        NSURLErrorTimedOut,
        NSURLErrorCannotFindHost,
        NSURLErrorCannotConnectToHost,
        NSURLErrorNetworkConnectionLost,
        NSURLErrorDNSLookupFailed,
        NSURLErrorNotConnectedToInternet,
        NSURLErrorInternationalRoamingOff,
        NSURLErrorCallIsActive,
        NSURLErrorDataNotAllowed
    ]

    private weak var webView: WKWebView?
    private weak var capacitorDelegate: WKNavigationDelegate?
    private let pathMonitor = NWPathMonitor()
    private let pathQueue = DispatchQueue(label: "com.ealeixandre.moa.server-reachability")
    private let banner = UIView()
    private var pathStatus: NWPath.Status
    private var retryURL: URL?
    private var retryIndex = 0
    private var retryWorkItem: DispatchWorkItem?

    init(webView: WKWebView, forwardingTo capacitorDelegate: WKNavigationDelegate) {
        self.webView = webView
        self.capacitorDelegate = capacitorDelegate
        self.pathStatus = pathMonitor.currentPath.status
        super.init()
        installBanner(in: webView)
        pathMonitor.pathUpdateHandler = { [weak self] path in
            DispatchQueue.main.async {
                self?.pathDidChange(to: path.status)
            }
        }
        pathMonitor.start(queue: pathQueue)
        NotificationCenter.default.addObserver(
            self,
            selector: #selector(applicationDidBecomeActive),
            name: UIApplication.didBecomeActiveNotification,
            object: nil
        )
    }

    deinit {
        retryWorkItem?.cancel()
        pathMonitor.cancel()
        NotificationCenter.default.removeObserver(self)
    }

    override func responds(to aSelector: Selector!) -> Bool {
        super.responds(to: aSelector) || capacitorDelegate?.responds(to: aSelector) == true
    }

    override func forwardingTarget(for aSelector: Selector!) -> Any? {
        if capacitorDelegate?.responds(to: aSelector) == true {
            return capacitorDelegate
        }
        return super.forwardingTarget(for: aSelector)
    }

    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
        capacitorDelegate?.webView?(webView, didFinish: navigation)
        guard let url = webView.url else { return }
        if NativeServerBinding.matches(url) {
            stopRetrying()
        } else if let retryURL, !NativeServerBinding.matches(retryURL) {
            stopRetrying()
        }
        updateBanner()
    }

    func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) {
        capacitorDelegate?.webView?(webView, didFail: navigation, withError: error)
        recoverIfNeeded(from: error)
    }

    func webView(
        _ webView: WKWebView,
        didFailProvisionalNavigation navigation: WKNavigation!,
        withError error: Error
    ) {
        capacitorDelegate?.webView?(webView, didFailProvisionalNavigation: navigation, withError: error)
        recoverIfNeeded(from: error)
    }

    private func recoverIfNeeded(from error: Error) {
        let error = error as NSError
        guard
            error.domain == NSURLErrorDomain,
            Self.retryableTransportErrors.contains(error.code),
            let url = failedURL(from: error) ?? webView?.url,
            NativeServerBinding.matches(url)
        else { return }

        if retryURL != url {
            retryURL = url
            retryIndex = 0
        }
        updateBanner()
        scheduleRetry()
    }

    private func failedURL(from error: NSError) -> URL? {
        if let url = error.userInfo[NSURLErrorFailingURLErrorKey] as? URL {
            return url
        }
        if let value = error.userInfo[NSURLErrorFailingURLStringErrorKey] as? String {
            return URL(string: value)
        }
        return nil
    }

    private func scheduleRetry() {
        guard retryWorkItem == nil, retryURL != nil, pathStatus == .satisfied else { return }
        let delay = Self.retryDelays[min(retryIndex, Self.retryDelays.count - 1)]
        retryIndex = min(retryIndex + 1, Self.retryDelays.count - 1)
        let workItem = DispatchWorkItem { [weak self] in
            guard let self else { return }
            self.retryWorkItem = nil
            self.retryNow()
        }
        retryWorkItem = workItem
        DispatchQueue.main.asyncAfter(deadline: .now() + delay, execute: workItem)
    }

    private func retryNow() {
        guard
            pathStatus == .satisfied,
            let webView,
            !webView.isLoading,
            let retryURL,
            NativeServerBinding.matches(retryURL)
        else {
            if let retryURL, !NativeServerBinding.matches(retryURL) {
                stopRetrying()
            }
            return
        }
        var request = URLRequest(
            url: retryURL,
            cachePolicy: .reloadIgnoringLocalCacheData,
            timeoutInterval: 10
        )
        request.httpMethod = "GET"
        webView.load(request)
    }

    private func stopRetrying() {
        retryWorkItem?.cancel()
        retryWorkItem = nil
        retryURL = nil
        retryIndex = 0
    }

    private func pathDidChange(to status: NWPath.Status) {
        pathStatus = status
        // A path change alone does not reload a still-live page: the frontend
        // reconnects its sockets on `online`, and reloading here would discard
        // an unsent composer draft. Reachability only accelerates a navigation
        // that has already failed at the transport layer.
        if status == .satisfied, retryURL != nil {
            retryWorkItem?.cancel()
            retryWorkItem = nil
            retryNow()
        } else if status != .satisfied {
            retryWorkItem?.cancel()
            retryWorkItem = nil
        }
        updateBanner()
    }

    @objc private func applicationDidBecomeActive() {
        guard retryURL != nil, pathStatus == .satisfied else {
            updateBanner()
            return
        }
        retryWorkItem?.cancel()
        retryWorkItem = nil
        retryNow()
    }

    private func installBanner(in webView: WKWebView) {
        banner.translatesAutoresizingMaskIntoConstraints = false
        banner.backgroundColor = UIColor(red: 49 / 255, green: 50 / 255, blue: 68 / 255, alpha: 0.94)
        banner.layer.cornerRadius = 14
        banner.isHidden = true
        banner.isUserInteractionEnabled = false

        let label = UILabel()
        label.translatesAutoresizingMaskIntoConstraints = false
        label.text = "Reconnecting…"
        label.textColor = UIColor(red: 205 / 255, green: 214 / 255, blue: 244 / 255, alpha: 1)
        label.font = .systemFont(ofSize: 13, weight: .medium)
        banner.addSubview(label)
        webView.addSubview(banner)

        NSLayoutConstraint.activate([
            banner.topAnchor.constraint(equalTo: webView.safeAreaLayoutGuide.topAnchor, constant: 8),
            banner.centerXAnchor.constraint(equalTo: webView.centerXAnchor),
            label.topAnchor.constraint(equalTo: banner.topAnchor, constant: 5),
            label.bottomAnchor.constraint(equalTo: banner.bottomAnchor, constant: -5),
            label.leadingAnchor.constraint(equalTo: banner.leadingAnchor, constant: 12),
            label.trailingAnchor.constraint(equalTo: banner.trailingAnchor, constant: -12)
        ])
    }

    private func updateBanner() {
        let pairedPageIsOffline = pathStatus != .satisfied
            && webView?.url.map(NativeServerBinding.matches) == true
        banner.isHidden = retryURL == nil && !pairedPageIsOffline
        if !banner.isHidden, let webView {
            webView.bringSubviewToFront(banner)
        }
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
