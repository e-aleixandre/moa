import AVFoundation
import WebKit
import os

// Keeps a voice call going with the screen locked and routes it to
// headphones or the car's hands-free. WebKit owns capture and playback; this
// only shapes the app's shared AVAudioSession while the WebView holds the
// microphone. Continuing in the background relies on UIBackgroundModes=audio
// in Info.plist, which WebKit reads to stop interrupting capture.
@MainActor
final class VoiceAudioSessionController {
    nonisolated private static let log = Logger(subsystem: "com.ealeixandre.moa.app", category: "voice-audio")

    private weak var webView: WKWebView?
    private var observation: NSKeyValueObservation?
    private var routeObserver: NSObjectProtocol?
    private var prior: (AVAudioSession.Category, AVAudioSession.Mode, AVAudioSession.CategoryOptions)?

    func attach(to webView: WKWebView) {
        self.webView = webView
        observation = webView.observe(\.microphoneCaptureState, options: [.initial, .new]) { [weak self] webView, _ in
            let capturing = webView.microphoneCaptureState != .none
            Task { @MainActor in self?.update(capturing: capturing) }
        }
    }

    private func update(capturing: Bool) {
        if capturing, prior == nil {
            begin()
        } else if !capturing, prior != nil {
            end()
        }
    }

    private func begin() {
        let session = AVAudioSession.sharedInstance()
        prior = (session.category, session.mode, session.categoryOptions)
        // allowBluetoothHFP is hands-free: the only Bluetooth route with a
        // microphone, which a car needs. A2DP stays allowed for output-only
        // headsets; the system prefers HFP when both are offered.
        // defaultToSpeaker only applies to the built-in route, so a call
        // without headphones is not played through the earpiece.
        do {
            try session.setCategory(.playAndRecord, mode: .voiceChat,
                                    options: [.allowBluetoothHFP, .allowBluetoothA2DP, .defaultToSpeaker])
        } catch {
            Self.log.error("voice session category: \(error.localizedDescription, privacy: .public)")
        }
        // Older frontends do not set the DOM session type themselves; WebKit
        // needs play-and-record to keep Web Audio running in the background.
        webView?.evaluateJavaScript("if (navigator.audioSession && navigator.audioSession.type === 'auto') navigator.audioSession.type = 'play-and-record';")
        routeObserver = NotificationCenter.default.addObserver(
            forName: AVAudioSession.routeChangeNotification, object: nil, queue: .main
        ) { note in
            let reason = (note.userInfo?[AVAudioSessionRouteChangeReasonKey] as? UInt) ?? 0
            let route = AVAudioSession.sharedInstance().currentRoute
            let outputs = route.outputs.map { $0.portType.rawValue }.joined(separator: ",")
            let inputs = route.inputs.map { $0.portType.rawValue }.joined(separator: ",")
            Self.log.info("voice route change \(reason, privacy: .public): in=\(inputs, privacy: .public) out=\(outputs, privacy: .public)")
        }
        Self.log.info("voice session on: \(session.currentRoute.outputs.map { $0.portType.rawValue }.joined(separator: ","), privacy: .public)")
    }

    private func end() {
        if let routeObserver { NotificationCenter.default.removeObserver(routeObserver) }
        routeObserver = nil
        guard let (category, mode, options) = prior else { return }
        prior = nil
        webView?.evaluateJavaScript("if (navigator.audioSession) navigator.audioSession.type = 'auto';")
        do {
            try AVAudioSession.sharedInstance().setCategory(category, mode: mode, options: options)
        } catch {
            Self.log.error("voice session restore: \(error.localizedDescription, privacy: .public)")
        }
        Self.log.info("voice session off")
    }
}
