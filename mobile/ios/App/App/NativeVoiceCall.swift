import AVFoundation
import CallKit
import WebKit
import os

private let callLog = Logger(subsystem: "com.ealeixandre.moa.app", category: "voice-call")

// `window.MoaNativeCall` for the paired frontend's voice calls. The call's
// WebSocket, microphone and speaker run here, in the app process, as a
// CallKit call: it goes on with the screen locked, routes to headphones and
// the car, and can be muted or hung up from the lock screen, a headset or
// CarPlay, whether or not the page is running. The page only mirrors the
// JSON control frames. Only the main frame of the paired origin may call it,
// and it may only open that origin's /api/voice/calls/{id}/ws.
final class NativeCallMessageHandler: NSObject, WKScriptMessageHandler {
    static let name = "moaNativeCall"
    static let javaScript = #"""
    (() => {
      if (window.MoaNativeCall) return;
      const post = (m) => window.webkit.messageHandlers.moaNativeCall.postMessage(m);
      const audio = new Map();
      const snapshots = [];
      // A pending startAudio settles once: with the app's answer, or
      // rejected when its call goes away first.
      const settle = (id, result) => {
        const p = audio.get(id);
        if (!p) return;
        audio.delete(id);
        if (result === "ok") p.resolve(); else p.reject(new Error(result || "microphone"));
      };
      window.MoaNativeCall = {
        version: 1,
        onevent: null,
        open(url, opts) {
          const id = "n" + Date.now().toString(36) + Math.random().toString(36).slice(2, 8);
          post({
            op: "open", id, url: String(url),
            name: String((opts && opts.name) || "moa"),
            sessionId: String((opts && opts.sessionId) || "")
          });
          return id;
        },
        send(id, text) { post({ op: "send", id, text: String(text) }); },
        close(id) { settle(id, "closed"); post({ op: "close", id }); },
        active() {
          return new Promise((resolve) => {
            snapshots.push(resolve);
            post({ op: "active", id: "" });
          });
        },
        startAudio(id) {
          return new Promise((resolve, reject) => {
            audio.set(id, { resolve, reject });
            post({ op: "startAudio", id });
          });
        },
        stopAudio(id) { settle(id, "stopped"); post({ op: "stopAudio", id }); },
        setMuted(id, muted) { post({ op: "setMuted", id, muted: !!muted }); },
        __event(ev) {
          if (ev.type === "audio") {
            settle(ev.id, ev.data);
            return;
          }
          if (ev.type === "active") {
            snapshots.splice(0).forEach((resolve) => resolve(ev.data || null));
            return;
          }
          if (ev.type === "close") settle(ev.id, "closed");
          if (typeof this.onevent === "function") this.onevent(ev);
        }
      };
    })();
    """#

    weak var webView: WKWebView? {
        didSet { NativeVoiceCallManager.shared.webView = webView }
    }

    func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
        guard
            message.frameInfo.isMainFrame,
            NativeServerBinding.matches(message.frameInfo.securityOrigin),
            let body = message.body as? [String: Any],
            let op = body["op"] as? String,
            let id = body["id"] as? String
        else { return }
        let manager = NativeVoiceCallManager.shared
        switch op {
        case "open":
            manager.open(id: id, url: body["url"] as? String ?? "", name: body["name"] as? String ?? "moa",
                         sessionId: body["sessionId"] as? String ?? "")
        case "active":
            manager.reportActive()
        case "send":
            if let text = body["text"] as? String { manager.send(id: id, text: text) }
        case "close":
            manager.closeFromPage(id: id)
        case "startAudio":
            manager.startAudio(id: id)
        case "stopAudio":
            manager.stopAudio(id: id)
        case "setMuted":
            manager.setMutedFromPage(id: id, muted: body["muted"] as? Bool ?? false)
        default:
            break
        }
    }
}

// One voice call at a time, as the server allows. Everything here runs on
// the main queue (CallKit and URLSession delegates are bound to it) except
// the microphone tap, which only sends frames.
final class NativeVoiceCallManager: NSObject, CXProviderDelegate, URLSessionWebSocketDelegate {
    static let shared = NativeVoiceCallManager()

    weak var webView: WKWebView?

    private final class Call {
        let id: String
        let uuid: UUID
        let incoming: Bool
        let name: String
        let sessionId: String
        let callId: String
        let origin: String
        let task: URLSessionWebSocketTask
        var audio: NativeCallAudio?
        var audioWanted = false
        var sessionActive = false
        var muted = false
        var manualSession = false
        var started = false
        var categoryError = ""
        var pending: [Data] = []
        var pendingBytes = 0
        var open = false
        var pageClosed = false
        var serverEnded = false
        var hangingUp = false
        var finished = false
        init(id: String, name: String, sessionId: String, callId: String, origin: String, task: URLSessionWebSocketTask,
             uuid: UUID = UUID(), incoming: Bool = false) {
            self.id = id
            self.uuid = uuid
            self.incoming = incoming
            self.name = name
            self.sessionId = sessionId
            self.callId = callId
            self.origin = origin
            self.task = task
        }
    }

    // Audio that arrives before the speaker is ready is kept, bounded, as
    // the page's engine does (PENDING_MAX_SECONDS of 24 kHz s16le).
    private static let pendingMaxBytes = 24000 * 2 * 4

    let provider: CXProvider
    private let callController = CXCallController()
    private lazy var session = URLSession(configuration: .default, delegate: self, delegateQueue: .main)
    private var call: Call?

    private override init() {
        let config = CXProviderConfiguration()
        config.supportsVideo = false
        config.maximumCallGroups = 1
        config.maximumCallsPerCallGroup = 1
        config.supportedHandleTypes = [.generic]
        config.includesCallsInRecents = false
        provider = CXProvider(configuration: config)
        super.init()
        provider.setDelegate(self, queue: nil)
    }

    // MARK: Page operations

    func open(id: String, url raw: String, name: String, sessionId: String) {
        if let existing = call { finish(existing, remote: false, why: "replaced") }
        guard let url = Self.callURL(raw), let origin = NativeServerBinding.origin, let credential = Self.credential() else {
            callLog.error("voice call refused: bad url or no paired credential")
            emitAny(id, "close")
            return
        }
        var request = URLRequest(url: url)
        request.setValue("Moa-Device \(credential)", forHTTPHeaderField: "Authorization")
        let task = session.webSocketTask(with: request)
        let call = Call(id: id, name: String(name.prefix(80)), sessionId: String(sessionId.prefix(128)),
                        callId: url.pathComponents.dropLast().last ?? "", origin: origin, task: task)
        self.call = call
        task.resume()
        receive(call)
        let handle = CXHandle(type: .generic, value: call.name)
        let start = CXStartCallAction(call: call.uuid, handle: handle)
        callController.request(CXTransaction(action: start)) { [weak self] error in
            guard let error else { return }
            // Without a system call (another call is on, or CallKit is not
            // available) the call still runs, but only while the app is open.
            callLog.error("CallKit start failed: \(error.localizedDescription, privacy: .public)")
            DispatchQueue.main.async { self?.activateWithoutCallKit(call, callKitError: Self.code(error)) }
        }
        callLog.info("voice call opening")
    }

    // MARK: Incoming calls (NativeIncomingCalls)

    // reportIncoming shows a ringing call at once; nothing about it runs
    // until it is answered.
    func reportIncoming(uuid: UUID, name: String, completion: @escaping (Error?) -> Void) {
        let update = CXCallUpdate()
        update.localizedCallerName = name
        update.remoteHandle = CXHandle(type: .generic, value: name)
        update.hasVideo = false
        update.supportsHolding = false
        update.supportsGrouping = false
        update.supportsUngrouping = false
        update.supportsDTMF = false
        provider.reportNewIncomingCall(with: uuid, update: update) { error in
            DispatchQueue.main.async { completion(error) }
        }
    }

    // reportFailedIncoming meets PushKit's duty to report a push that is not
    // a call this app can ring (a registration challenge, an envelope that
    // does not open): a call shown and ended as failed at once.
    func reportFailedIncoming(name: String, completion: @escaping () -> Void) {
        let uuid = UUID()
        reportIncoming(uuid: uuid, name: name) { [weak self] _ in
            self?.provider.reportCall(with: uuid, endedAt: Date(), reason: .failed)
            completion()
        }
    }

    // startIncoming runs an answered invitation's call like an outgoing one:
    // same socket, engine and teardown. The system already has the call.
    func startIncoming(uuid: UUID, name: String, sessionId: String, origin: String, url: URL, authorization: String) -> Bool {
        if let existing = call { finish(existing, remote: false, why: "replaced") }
        guard let url = Self.callURL(url.absoluteString) else { return false }
        var request = URLRequest(url: url)
        request.setValue(authorization, forHTTPHeaderField: "Authorization")
        let task = session.webSocketTask(with: request)
        let call = Call(id: "in-" + uuid.uuidString, name: String(name.prefix(80)), sessionId: String(sessionId.prefix(128)),
                        callId: url.pathComponents.dropLast().last ?? "", origin: origin, task: task, uuid: uuid, incoming: true)
        call.started = true
        // Answering is the consent to the microphone; it cannot be asked for
        // on a locked screen, so a call without it ends with that reason.
        call.audioWanted = AVAudioSession.sharedInstance().recordPermission == .granted
        self.call = call
        task.resume()
        receive(call)
        if !call.audioWanted {
            DispatchQueue.main.async { self.finish(call, remote: false, why: "mic_not_granted") }
        }
        callLog.info("incoming voice call answered")
        return true
    }

    static func configureSessionForCall() {
        do {
            try configureSession()
        } catch {
            callLog.error("voice session category: \(error.localizedDescription, privacy: .public)")
        }
    }

    // The live call, if this id names it and the app is still paired with
    // the server that call belongs to.
    private func current(_ id: String) -> Call? {
        guard let call, call.id == id, call.origin == NativeServerBinding.origin else { return nil }
        return call
    }

    // reportActive answers a new document's question: is a call still on?
    func reportActive() {
        var snapshot: Any = NSNull()
        if let call, !call.finished, call.origin == NativeServerBinding.origin {
            snapshot = ["id": call.id, "sessionId": call.sessionId, "callId": call.callId, "muted": call.muted]
        }
        emitAny("", "active", snapshot)
    }

    // bindingChanged ends a call whose server is no longer the paired one
    // (unpaired, or paired elsewhere): its frames must not reach another.
    func bindingChanged() {
        guard let call, call.origin != NativeServerBinding.origin else { return }
        call.pageClosed = true
        finish(call, remote: false, why: "unpaired")
    }

    func send(id: String, text: String) {
        guard let call = current(id), call.open else { return }
        call.task.send(.string(text)) { _ in }
    }

    func closeFromPage(id: String) {
        guard let call = current(id) else { return }
        call.pageClosed = true
        finish(call, remote: false, why: "page_closed")
    }

    func startAudio(id: String) {
        guard let call = current(id) else {
            emitAny(id, "audio", "microphone")
            return
        }
        if call.audio != nil {
            // A document that adopted the running call.
            emit(call, "audio", "ok")
            return
        }
        call.audioWanted = true
        AVAudioSession.sharedInstance().requestRecordPermission { [weak self] granted in
            DispatchQueue.main.async {
                guard let self, let current = self.call, current === call else { return }
                guard granted else {
                    self.emit(call, "audio", "microphone")
                    self.finish(call, remote: false, why: "mic_denied")
                    return
                }
                if call.sessionActive { self.startEngine(call) }
            }
        }
    }

    func stopAudio(id: String) {
        guard let call = current(id) else { return }
        call.audioWanted = false
        call.audio?.stop()
        call.audio = nil
    }

    private func interrupt(_ call: Call) {
        call.pending = []
        call.pendingBytes = 0
        call.audio?.interrupt()
    }

    // Every request goes to CallKit in order, even one that equals the state
    // applied so far: an earlier request may still be pending.
    func setMutedFromPage(id: String, muted: Bool) {
        guard let call = current(id) else { return }
        let action = CXSetMutedCallAction(call: call.uuid, muted: muted)
        callController.request(CXTransaction(action: action)) { [weak self] error in
            guard error != nil else { return }
            DispatchQueue.main.async { self?.applyMute(call, muted: muted, fromSystem: false) }
        }
    }

    // MARK: Call lifecycle

    private func receive(_ call: Call) {
        call.task.receive { [weak self] result in
            guard let self, self.call === call else { return }
            switch result {
            case .failure(let error):
                self.finish(call, remote: true, why: "receive \(Self.code(error))")
            case .success(.data(let data)):
                self.play(call, data)
                self.receive(call)
            case .success(.string(let text)):
                self.control(call, text)
                self.emit(call, "message", text)
                self.receive(call)
            @unknown default:
                self.receive(call)
            }
        }
    }

    private func control(_ call: Call, _ text: String) {
        guard
            let data = text.data(using: .utf8),
            let msg = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
            let type = msg["type"] as? String
        else { return }
        switch type {
        case "speech_started":
            interrupt(call)
        case "state":
            if msg["interrupt"] as? Bool == true { interrupt(call) }
        case "ended":
            call.serverEnded = true
        default:
            break
        }
    }

    private func play(_ call: Call, _ data: Data) {
        if let audio = call.audio {
            audio.play(data)
            return
        }
        call.pending.append(data)
        call.pendingBytes += data.count
        while call.pendingBytes > Self.pendingMaxBytes, !call.pending.isEmpty {
            call.pendingBytes -= call.pending.removeFirst().count
        }
    }

    private func startEngine(_ call: Call) {
        guard call.audioWanted, call.audio == nil, !call.finished else { return }
        let audio = NativeCallAudio()
        audio.muted = call.muted
        audio.onFrame = NativeCallAudio.sender(for: call.task)
        audio.onPlayback = { [weak self] event in self?.emit(call, "playback", event) }
        audio.onFailure = { [weak self] in
            guard let self, self.call === call else { return }
            callLog.error("voice audio lost after a route change")
            self.finish(call, remote: false, why: "route_rebuild")
        }
        do {
            try audio.start()
        } catch {
            callLog.error("voice audio start: \(error.localizedDescription, privacy: .public)")
            emit(call, "audio", "microphone")
            finish(call, remote: false, why: "engine \(Self.code(error)) category \(call.categoryError.isEmpty ? "ok" : call.categoryError) route \(AVAudioSession.sharedInstance().currentRoute.inputs.first?.portType.rawValue ?? "none")")
            return
        }
        call.audio = audio
        // Ready first: the page shows "speaking" only for playback it can see.
        emit(call, "audio", "ok")
        for chunk in call.pending { audio.play(chunk) }
        call.pending = []
        call.pendingBytes = 0
        callLog.info("voice audio on: \(AVAudioSession.sharedInstance().currentRoute.outputs.map { $0.portType.rawValue }.joined(separator: ","), privacy: .public)")
    }

    private func applyMute(_ call: Call, muted: Bool, fromSystem: Bool) {
        call.muted = muted
        call.audio?.muted = muted
        if call.open {
            let text = "{\"type\":\"mute\",\"muted\":\(muted)}"
            call.task.send(.string(text)) { _ in }
        }
        if fromSystem { emit(call, "mute", muted) }
    }

    // finish ends the call once: audio off, socket closed, system call ended
    // and, unless the page closed it itself, the page told.
    // why travels as the WebSocket close reason, which the server logs: the
    // only way to learn on a device with no cable why the app ended a call.
    private func finish(_ call: Call, remote: Bool, why: String = "") {
        guard !call.finished else { return }
        call.finished = true
        call.audio?.stop()
        call.audio = nil
        let reason = String("native:\(why.isEmpty ? "unspecified" : why) mic=\(Self.micPermission())".prefix(120))
        callLog.error("voice call ending: \(reason, privacy: .public)")
        call.task.cancel(with: .normalClosure, reason: reason.data(using: .utf8))
        if call.manualSession {
            try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
        }
        if remote {
            provider.reportCall(with: call.uuid, endedAt: Date(), reason: call.serverEnded || call.hangingUp ? .remoteEnded : .failed)
        } else {
            callController.request(CXTransaction(action: CXEndCallAction(call: call.uuid))) { _ in }
        }
        if !call.pageClosed { emit(call, "close", reason) }
        if self.call === call { self.call = nil }
        callLog.info("voice call finished remote=\(remote, privacy: .public) server_ended=\(call.serverEnded, privacy: .public)")
    }

    private func activateWithoutCallKit(_ call: Call, callKitError: String) {
        guard self.call === call, !call.sessionActive else { return }
        do {
            try Self.configureSession()
            try AVAudioSession.sharedInstance().setActive(true)
            call.manualSession = true
            call.sessionActive = true
            if call.audioWanted { startEngine(call) }
        } catch {
            callLog.error("voice session without CallKit: \(error.localizedDescription, privacy: .public)")
            emit(call, "audio", "microphone")
            finish(call, remote: false, why: "callkit \(callKitError) session \(Self.code(error))")
        }
    }

    private static func configureSession() throws {
        // allowBluetoothHFP is hands-free, the only Bluetooth route with a
        // microphone (headsets, the car); defaultToSpeaker keeps a call
        // without headphones on the loudspeaker, as on the web.
        try AVAudioSession.sharedInstance().setCategory(
            .playAndRecord, mode: .voiceChat,
            options: [.allowBluetoothHFP, .allowBluetoothA2DP, .defaultToSpeaker]
        )
    }

    // MARK: CXProviderDelegate

    func providerDidReset(_ provider: CXProvider) {
        NativeIncomingCalls.shared.reset()
        if let call { finish(call, remote: false, why: "provider_reset") }
    }

    func provider(_ provider: CXProvider, perform action: CXStartCallAction) {
        guard let call, call.uuid == action.callUUID else {
            action.fail()
            return
        }
        do {
            try Self.configureSession()
        } catch {
            // Not fatal: CallKit still activates a session; if the engine
            // cannot run on it, that failure ends the call with its own reason.
            callLog.error("voice session category: \(error.localizedDescription, privacy: .public)")
            call.categoryError = Self.code(error)
        }
        call.started = true
        provider.reportOutgoingCall(with: call.uuid, startedConnectingAt: Date())
        let update = CXCallUpdate()
        update.localizedCallerName = call.name
        update.remoteHandle = CXHandle(type: .generic, value: call.name)
        update.hasVideo = false
        update.supportsHolding = false
        update.supportsGrouping = false
        update.supportsUngrouping = false
        update.supportsDTMF = false
        provider.reportCall(with: call.uuid, updated: update)
        if call.open { provider.reportOutgoingCall(with: call.uuid, connectedAt: Date()) }
        action.fulfill()
    }

    func provider(_ provider: CXProvider, perform action: CXAnswerCallAction) {
        if !NativeIncomingCalls.shared.answer(action) { action.fail() }
    }

    func provider(_ provider: CXProvider, perform action: CXEndCallAction) {
        if NativeIncomingCalls.shared.decline(action) { return }
        action.fulfill()
        guard let call, call.uuid == action.callUUID, !call.finished else { return }
        // Hung up from the lock screen, a headset or the car: silence now,
        // and let the server end the call so its record is saved; the page
        // learns it from the server's "ended".
        call.audioWanted = false
        call.audio?.stop()
        call.audio = nil
        call.hangingUp = true
        if call.open {
            call.task.send(.string("{\"type\":\"hangup\"}")) { _ in }
            DispatchQueue.main.asyncAfter(deadline: .now() + 5) { [weak self] in
                guard let self, self.call === call else { return }
                self.finish(call, remote: true, why: "system_hangup_timeout")
            }
        } else {
            finish(call, remote: true, why: "system_hangup_before_open")
        }
    }

    func provider(_ provider: CXProvider, perform action: CXSetMutedCallAction) {
        action.fulfill()
        guard let call, call.uuid == action.callUUID else { return }
        applyMute(call, muted: action.isMuted, fromSystem: true)
    }

    func provider(_ provider: CXProvider, didActivate audioSession: AVAudioSession) {
        guard let call else { return }
        call.sessionActive = true
        if call.audioWanted { startEngine(call) }
    }

    func provider(_ provider: CXProvider, didDeactivate audioSession: AVAudioSession) {
        guard let call else { return }
        call.sessionActive = false
        call.audio?.stop()
        call.audio = nil
    }

    // MARK: URLSessionWebSocketDelegate

    func urlSession(_ session: URLSession, webSocketTask: URLSessionWebSocketTask, didOpenWithProtocol protocol: String?) {
        guard let call, call.task === webSocketTask else { return }
        call.open = true
        // Connected is reported only for an outgoing call CallKit has started.
        if call.started && !call.incoming { provider.reportOutgoingCall(with: call.uuid, connectedAt: Date()) }
        emit(call, "open")
    }

    func urlSession(_ session: URLSession, webSocketTask: URLSessionWebSocketTask, didCloseWith closeCode: URLSessionWebSocketTask.CloseCode, reason: Data?) {
        guard let call, call.task === webSocketTask else { return }
        finish(call, remote: true, why: "server_closed \(closeCode.rawValue)")
    }

    // Only the validated call URL: no redirect to another path or origin.
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) {
        completionHandler(nil)
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
        guard let call, call.task === task else { return }
        if let error { callLog.error("voice socket: \(error.localizedDescription, privacy: .public)") }
        finish(call, remote: true, why: "socket \(error.map(Self.code) ?? "done")")
    }

    // MARK: Helpers

    // emit tells the page about a call, only while the page shown is that
    // call's server and the app is still paired with it.
    private func emit(_ call: Call, _ type: String, _ data: Any? = nil) {
        guard
            call.origin == NativeServerBinding.origin,
            let url = webView?.url, NativeServerBinding.matches(url)
        else { return }
        emitAny(call.id, type, data)
    }

    private func emitAny(_ id: String, _ type: String, _ data: Any? = nil) {
        var event: [String: Any] = ["id": id, "type": type]
        if let data { event["data"] = data }
        guard
            let json = try? JSONSerialization.data(withJSONObject: event),
            let text = String(data: json, encoding: .utf8)
        else { return }
        webView?.evaluateJavaScript("window.MoaNativeCall && window.MoaNativeCall.__event(\(text));")
    }

    static func code(_ error: Error) -> String {
        let e = error as NSError
        return "\(e.domain.replacingOccurrences(of: "com.apple.", with: "")):\(e.code)"
    }

    static func micPermission() -> String {
        switch AVAudioSession.sharedInstance().recordPermission {
        case .granted: return "granted"
        case .denied: return "denied"
        default: return "undetermined"
        }
    }

    // callURL accepts only the paired server's own voice call socket.
    static func callURL(_ raw: String) -> URL? {
        guard
            var components = URLComponents(string: raw),
            components.scheme?.lowercased() == "wss",
            components.user == nil, components.password == nil,
            components.query == nil, components.fragment == nil
        else { return nil }
        let parts = components.path.split(separator: "/", omittingEmptySubsequences: false)
        guard
            parts.count == 6, parts[0].isEmpty, parts[1] == "api", parts[2] == "voice",
            parts[3] == "calls", !parts[4].isEmpty, parts[5] == "ws"
        else { return nil }
        components.scheme = "https"
        guard let https = components.url, NativeServerBinding.matches(https) else { return nil }
        components.scheme = "wss"
        return components.url
    }

    private static func credential() -> String? {
        guard
            let stored = try? DeviceCredentialStore().load(),
            let origin = URL(string: stored.origin),
            NativeServerBinding.matches(origin),
            stored.expiresAt > Date()
        else { return nil }
        return stored.credential
    }
}

// The call's microphone and speaker: voice processing (echo cancellation)
// on, s16le mono 24 kHz in 20 ms frames out, the provider's 24 kHz played
// back. A route change (AirPods, the car) rebuilds the graph.
final class NativeCallAudio {
    private static let frameBytes = 480 * 2
    // The page's bounds: one second of captured audio in flight
    // (MAX_SOCKET_BUFFERED) and 60 s queued to play (PLAYBACK_MAX_SECONDS).
    private static let maxSendingBytes = 48000
    private static let maxQueuedSamples = 24000 * 60

    var onFrame: ((Data) -> Void)?
    var onPlayback: ((String) -> Void)?
    var onFailure: (() -> Void)?
    var muted: Bool {
        get { lock.lock(); defer { lock.unlock() }; return mutedValue }
        set { lock.lock(); mutedValue = newValue; lock.unlock() }
    }

    private let engine = AVAudioEngine()
    private let player = AVAudioPlayerNode()
    private let wireFormat = AVAudioFormat(commonFormat: .pcmFormatInt16, sampleRate: 24000, channels: 1, interleaved: true)!
    private let playFormat = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: 24000, channels: 1, interleaved: false)!
    private let lock = NSLock()
    private var mutedValue = false
    private var converter: AVAudioConverter?
    private var frame = Data()
    private var queued = 0
    private var queuedSamples = 0
    private var generation = 0
    private var running = false
    private var configObserver: NSObjectProtocol?

    func start() throws {
        try engine.inputNode.setVoiceProcessingEnabled(true)
        engine.attach(player)
        engine.connect(player, to: engine.mainMixerNode, format: playFormat)
        try startGraph()
        running = true
        configObserver = NotificationCenter.default.addObserver(
            forName: .AVAudioEngineConfigurationChange, object: engine, queue: .main
        ) { [weak self] _ in self?.restart() }
    }

    private func startGraph() throws {
        let input = engine.inputNode
        let format = input.outputFormat(forBus: 0)
        guard format.sampleRate > 0, format.channelCount > 0 else {
            throw NSError(domain: "moa.voice", code: 1, userInfo: [NSLocalizedDescriptionKey: "no microphone input"])
        }
        guard let converter = AVAudioConverter(from: format, to: wireFormat) else {
            throw NSError(domain: "moa.voice", code: 2, userInfo: [NSLocalizedDescriptionKey: "no converter for \(format)"])
        }
        self.converter = converter
        input.removeTap(onBus: 0)
        input.installTap(onBus: 0, bufferSize: 1024, format: format) { [weak self] buffer, _ in
            self?.capture(buffer, converter)
        }
        engine.prepare()
        try engine.start()
        player.play()
    }

    private func restart() {
        guard running else { return }
        callLog.info("voice audio route changed: \(AVAudioSession.sharedInstance().currentRoute.outputs.map { $0.portType.rawValue }.joined(separator: ","), privacy: .public)")
        engine.stop()
        player.stop()
        let was = queued > 0
        generation += 1
        queued = 0
        queuedSamples = 0
        if was { onPlayback?("drained") }
        do {
            try startGraph()
        } catch {
            callLog.error("voice audio restart: \(error.localizedDescription, privacy: .public)")
            let failed = onFailure
            stop()
            failed?()
        }
    }

    // Runs on the audio thread.
    private func capture(_ buffer: AVAudioPCMBuffer, _ converter: AVAudioConverter?) {
        guard let converter else { return }
        let ratio = wireFormat.sampleRate / buffer.format.sampleRate
        let capacity = AVAudioFrameCount(Double(buffer.frameLength) * ratio) + 32
        guard let out = AVAudioPCMBuffer(pcmFormat: wireFormat, frameCapacity: capacity) else { return }
        var fed = false
        var error: NSError?
        converter.convert(to: out, error: &error) { _, status in
            if fed {
                status.pointee = .noDataNow
                return nil
            }
            fed = true
            status.pointee = .haveData
            return buffer
        }
        guard error == nil, out.frameLength > 0, let samples = out.int16ChannelData else { return }
        frame.append(Data(bytes: samples[0], count: Int(out.frameLength) * 2))
        let silent = muted
        while frame.count >= Self.frameBytes {
            let chunk = frame.prefix(Self.frameBytes)
            frame.removeFirst(Self.frameBytes)
            if !silent { onFrame?(Data(chunk)) }
        }
    }

    func play(_ data: Data) {
        let count = data.count / 2
        // Over the bound the new audio is dropped, as on the web.
        guard running, count > 0, queuedSamples + count <= Self.maxQueuedSamples,
              let buffer = AVAudioPCMBuffer(pcmFormat: playFormat, frameCapacity: AVAudioFrameCount(count)),
              let out = buffer.floatChannelData?[0]
        else { return }
        buffer.frameLength = AVAudioFrameCount(count)
        data.withUnsafeBytes { raw in
            for i in 0..<count {
                out[i] = Float(Int16(littleEndian: raw.loadUnaligned(fromByteOffset: i * 2, as: Int16.self))) / 32768
            }
        }
        let gen = generation
        if queued == 0 { onPlayback?("playing") }
        queued += 1
        queuedSamples += count
        player.scheduleBuffer(buffer, completionCallbackType: .dataPlayedBack) { [weak self] _ in
            DispatchQueue.main.async { self?.played(gen, count) }
        }
    }

    private func played(_ gen: Int, _ count: Int) {
        guard gen == generation, queued > 0 else { return }
        queued -= 1
        queuedSamples = max(0, queuedSamples - count)
        if queued == 0 { onPlayback?("drained") }
    }

    func interrupt() {
        guard running else { return }
        generation += 1
        let was = queued > 0
        queued = 0
        queuedSamples = 0
        player.stop()
        player.play()
        if was { onPlayback?("drained") }
    }

    func stop() {
        guard running else { return }
        running = false
        if let configObserver { NotificationCenter.default.removeObserver(configObserver) }
        configObserver = nil
        engine.inputNode.removeTap(onBus: 0)
        player.stop()
        engine.stop()
        onFrame = nil
        onPlayback = nil
        onFailure = nil
    }

    // sender sends captured frames on the call's socket, dropping fresh ones
    // while a second of audio is still in flight instead of queueing more.
    static func sender(for task: URLSessionWebSocketTask) -> (Data) -> Void {
        let lock = NSLock()
        var sending = 0
        return { frame in
            lock.lock()
            guard sending + frame.count <= maxSendingBytes else {
                lock.unlock()
                return
            }
            sending += frame.count
            lock.unlock()
            task.send(.data(frame)) { _ in
                lock.lock()
                sending -= frame.count
                lock.unlock()
            }
        }
    }
}
