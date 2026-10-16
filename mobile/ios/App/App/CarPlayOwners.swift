import Foundation
import UIKit

// The CarPlay owner list and call screen as plain data: what the server's
// voice directory becomes on the car screen, which alert a failure shows and
// which voice state a call event selects. No CarPlay types here, so XCTest
// runs it without a car; CarPlaySceneDelegate and CarPlayCall only render it.

// MARK: Texts

// The car never asks to pick up the iPhone (CarPlay guide p.5): an error
// names the condition and nothing else.
enum CarPlayText {
    static var title: String { NSLocalizedString("CARPLAY_TITLE", value: "moa", comment: "CarPlay list title") }
    static var closed: String { NSLocalizedString("CARPLAY_OWNER_CLOSED", value: "Closed", comment: "CarPlay: owner whose conversation is closed; calling reopens it") }
    static var notSetUp: String { NSLocalizedString("CARPLAY_NOT_SET_UP", value: "moa isn't set up for calls yet", comment: "CarPlay: this iPhone has no voice credential") }
    static var noPlan: String { NSLocalizedString("CARPLAY_ERROR_NO_PLAN", value: "No voice plan available", comment: "CarPlay alert") }
    static var callActive: String { NSLocalizedString("CARPLAY_ERROR_CALL_ACTIVE", value: "A call is already in progress", comment: "CarPlay alert") }
    static var unreachable: String { NSLocalizedString("CARPLAY_ERROR_UNREACHABLE", value: "Can't reach your moa server", comment: "CarPlay alert") }
    static var micOff: String { NSLocalizedString("CARPLAY_ERROR_MIC_OFF", value: "Microphone access is off for moa", comment: "CarPlay alert") }
    static var ok: String { NSLocalizedString("CARPLAY_OK", value: "OK", comment: "CarPlay alert button") }
    static var connecting: String { NSLocalizedString("CARPLAY_CONNECTING", value: "Connecting…", comment: "CarPlay voice state") }
    static var listening: String { NSLocalizedString("CARPLAY_LISTENING", value: "Listening", comment: "CarPlay voice state") }
    static var muted: String { NSLocalizedString("CARPLAY_MUTED", value: "Muted", comment: "CarPlay voice state") }
    static var end: String { NSLocalizedString("CARPLAY_END", value: "End", comment: "CarPlay: end the call") }
    static var mute: String { NSLocalizedString("CARPLAY_MUTE", value: "Mute", comment: "CarPlay: mute the microphone") }
    static var unmute: String { NSLocalizedString("CARPLAY_UNMUTE", value: "Unmute", comment: "CarPlay: unmute the microphone") }

    static func speaking(_ name: String) -> String {
        String(format: NSLocalizedString("CARPLAY_SPEAKING", value: "%@ is speaking", comment: "CarPlay voice state; %@ is the owner's name"), name)
    }

    static func message(_ alert: CarPlayAlert) -> String {
        switch alert {
        case .noPlan: return noPlan
        case .callActive: return callActive
        case .unreachable: return unreachable
        case .micOff: return micOff
        }
    }
}

enum CarPlayAlert: Equatable {
    case noPlan, callActive, unreachable, micOff
}

// MARK: Directory → list

struct CarPlayDirectory: Decodable, Equatable {
    struct Avatar: Decodable, Equatable {
        var shape: String?
        var color: String?
        var tone: String?
    }
    struct Owner: Decodable, Equatable {
        var id: String
        var name: String
        var avatar: Avatar?
        var state: String
    }
    var owners: [Owner]
}

struct CarPlayRow: Equatable {
    let id: String
    let name: String
    let subtitle: String?
    // false: the owner has no conversation to call (state "none").
    let enabled: Bool
    let avatar: CarPlayDirectory.Avatar?
}

enum CarPlayListContent: Equatable {
    case owners([CarPlayRow])
    // A single line instead of the owners.
    case message(String)
}

enum CarPlayListModel {
    // rows keeps the API's order and every owner, up to what the car shows.
    static func rows(_ directory: CarPlayDirectory, limit: Int) -> [CarPlayRow] {
        directory.owners.prefix(max(0, limit)).map { owner in
            switch owner.state {
            case "open":
                return CarPlayRow(id: owner.id, name: owner.name, subtitle: nil, enabled: true, avatar: owner.avatar)
            case "closed":
                return CarPlayRow(id: owner.id, name: owner.name, subtitle: CarPlayText.closed, enabled: true, avatar: owner.avatar)
            default:
                return CarPlayRow(id: owner.id, name: owner.name, subtitle: nil, enabled: false, avatar: owner.avatar)
            }
        }
    }

    // content answers what the list shows after a directory request.
    // status nil: no response (network); data is the body.
    static func content(hasCredential: Bool, status: Int?, data: Data?, limit: Int) -> CarPlayListContent {
        guard hasCredential else { return .message(CarPlayText.notSetUp) }
        switch status {
        case 200:
            guard let data, let directory = try? JSONDecoder().decode(CarPlayDirectory.self, from: data) else {
                return .message(CarPlayText.unreachable)
            }
            return .owners(rows(directory, limit: limit))
        case 401, 403:
            // The server no longer knows this iPhone's voice credential.
            return .message(CarPlayText.notSetUp)
        default:
            return .message(CarPlayText.unreachable)
        }
    }
}

// MARK: Creating the call

enum CarPlayCallStart: Equatable {
    case started(wsPath: String)
    case alert(CarPlayAlert)
    // The owner changed under the list (deleted, no conversation) or the
    // credential stopped working: back to a fresh list, which says so.
    case reload
}

enum CarPlayCallStartModel {
    static func outcome(status: Int?, data: Data?) -> CarPlayCallStart {
        guard let status else { return .alert(.unreachable) }
        let body = data.flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Any] } ?? [:]
        let error = body["error"] as? String ?? ""
        switch (status, error) {
        case (201, _):
            guard let ws = body["ws_url"] as? String, ws.hasPrefix("/api/voice/calls/"), ws.hasSuffix("/ws") else {
                return .alert(.unreachable)
            }
            return .started(wsPath: ws)
        case (409, "owner_busy"):
            // The owner is being reopened by another request: retryable, but
            // the car does not retry on its own.
            return .alert(.unreachable)
        case (409, "call_active"):
            return .alert(.callActive)
        case (503, "plan_unavailable"), (503, "no_plan"), (502, "provider_unavailable"):
            // No payment is offered in the car, even when the server names a
            // paid alternative.
            return .alert(.noPlan)
        case (404, _), (409, "owner_has_no_conversation"), (401, _), (403, _):
            return .reload
        default:
            return .alert(.unreachable)
        }
    }
}

// MARK: Call events → voice state

enum CarPlayCallPhase: String, CaseIterable {
    case connecting, listening, speaking, muted
}

enum CarPlayCallEvent: Equatable {
    case open
    case playback(String)
    case mute(Bool)
    // userEnded: the user tapped End (or left the screen); serverEnded: the
    // server ended the call itself, which is not a failure either.
    case close(reason: String, serverEnded: Bool, userEnded: Bool)
}

enum CarPlayCallEffect: Equatable {
    case show(CarPlayCallPhase)
    // Back to the list, with an alert unless the end was expected.
    case end(alert: CarPlayAlert?)
}

struct CarPlayCallMachine {
    private(set) var opened = false
    private(set) var playing = false
    private(set) var muted = false
    private(set) var ended = false

    var phase: CarPlayCallPhase {
        if !opened { return .connecting }
        if playing { return .speaking }
        return muted ? .muted : .listening
    }

    mutating func apply(_ event: CarPlayCallEvent) -> CarPlayCallEffect? {
        guard !ended else { return nil }
        let before = phase
        switch event {
        case .open:
            opened = true
        case .playback(let state):
            if state == "playing" { playing = true } else if state == "drained" { playing = false }
        case .mute(let value):
            muted = value
        case .close(let reason, let serverEnded, let userEnded):
            ended = true
            if userEnded || serverEnded || reason.contains("interrupted") { return .end(alert: nil) }
            if reason.contains("mic_") { return .end(alert: .micOff) }
            return .end(alert: .unreachable)
        }
        return phase == before ? nil : .show(phase)
    }
}

// MARK: HTTP

// What the car asks the server. status nil: no response.
protocol CarPlayServer: AnyObject {
    var origin: String { get }
    func directory(_ done: @escaping (Int?, Data?) -> Void)
    func call(owner id: String, _ done: @escaping (Int?, Data?) -> Void)
    func socketURL(_ wsPath: String) -> URL?
    func invalidate()
}

// The two requests the car makes, with the locked iPhone's Moa-Voice
// credential. Never follows a redirect.
final class CarPlayClient: NSObject, URLSessionTaskDelegate, CarPlayServer {
    let origin: String
    private let authorization: String
    private lazy var session: URLSession = {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.httpCookieAcceptPolicy = .never
        configuration.httpShouldSetCookies = false
        configuration.timeoutIntervalForRequest = 15
        return URLSession(configuration: configuration, delegate: self, delegateQueue: .main)
    }()

    init(origin: String, authorization: String) {
        self.origin = origin
        self.authorization = authorization
    }

    func directory(_ done: @escaping (Int?, Data?) -> Void) {
        request("GET", "/api/voice/directory", body: nil, done)
    }

    func call(owner id: String, _ done: @escaping (Int?, Data?) -> Void) {
        let escaped = id.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed.subtracting(CharacterSet(charactersIn: "/"))) ?? ""
        request("POST", "/api/voice/owners/\(escaped)/call", body: Data("{}".utf8), done)
    }

    // The call's socket on the same server.
    func socketURL(_ wsPath: String) -> URL? {
        guard origin.hasPrefix("https://") else { return nil }
        return URL(string: "wss://" + origin.dropFirst("https://".count) + wsPath)
    }

    // A request in flight still completes: a call it creates must be
    // opened or hung up.
    func invalidate() {
        session.finishTasksAndInvalidate()
    }

    private func request(_ method: String, _ path: String, body: Data?, _ done: @escaping (Int?, Data?) -> Void) {
        guard let request = makeRequest(method, path, body: body) else {
            done(nil, nil)
            return
        }
        session.dataTask(with: request) { data, response, _ in
            done((response as? HTTPURLResponse)?.statusCode, data)
        }.resume()
    }

    // makeRequest is every request the car sends. A POST carries the
    // X-Moa-Request marker the server's CSRF check requires of every
    // non-GET, native credentials included.
    func makeRequest(_ method: String, _ path: String, body: Data?) -> URLRequest? {
        guard origin.hasPrefix("https://"), let url = URL(string: origin + path) else { return nil }
        var request = URLRequest(url: url)
        request.httpMethod = method
        request.setValue(authorization, forHTTPHeaderField: "Authorization")
        if method != "GET" { request.setValue("1", forHTTPHeaderField: "X-Moa-Request") }
        if let body {
            request.httpBody = body
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        return request
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) {
        completionHandler(nil)
    }
}

// MARK: Avatars

// The owner's face as the web draws it (pkg/serve/frontend
// avatar-identity.js): the shape's outline in a 32×32 box, filled with the
// identity colour at its tone, and two open eyes. No motion, no state.
enum CarPlayAvatar {
    static let shapes = [
        "circle", "squircle", "blob", "hexagon", "drop", "pill",
        "triangle", "cloud", "flower", "ghost", "bean", "diamond", "shield", "bell",
    ]

    // SHAPE_PATHS, copied verbatim.
    static let paths: [String: String] = [
        "circle": "M16 1.5a14.5 14.5 0 1 1 0 29 14.5 14.5 0 0 1 0-29Z",
        "squircle": "M16 1.5C26 1.5 30.5 6 30.5 16S26 30.5 16 30.5 1.5 26 1.5 16 6 1.5 16 1.5Z",
        "blob": "M17.3 1.9c6.3-.5 11.5 3.2 12.9 9.2 1.3 6-1.4 11.7-6.4 15.1-5 3.3-11.5 3-15.4-1.1C4.2 20.8 2.4 14 5.1 8.6 7.5 3.9 11.5 2.4 17.3 1.9Z",
        "hexagon": "M16 1.5 28.6 8.75v14.5L16 30.5 3.4 23.25V8.75Z",
        "drop": "M16 1.8c5.8 6 11.7 8.4 11.7 15.4a11.7 11.7 0 1 1-23.4 0c0-7 5.9-9.4 11.7-15.4Z",
        "pill": "M11.5 6h9a10 10 0 0 1 0 20h-9a10 10 0 0 1 0-20Z",
        "triangle": "M13.2 5.4Q16 .9 18.8 5.4L29.3 23.4Q31.9 28 26.6 28H5.4Q.1 28 2.7 23.4Z",
        "cloud": "M9.2 27.2A6.4 6.4 0 0 1 7.7 14.6 8.4 8.4 0 0 1 24 12.1 7.5 7.5 0 0 1 24.4 27.2Z",
        "ghost": "M16 2.5C23.2 2.5 28 7.8 28 15v13.4q-2-2.8-4 0t-4 0-4 0-4 0-4 0-4 0V15C4 7.8 8.8 2.5 16 2.5Z",
        "bean": "M10.4 4.3C14.6 2.8 17.4 6 21.6 5.4 26.8 4.8 30.2 9.4 29.8 15.6 29.4 23.4 23.8 28.6 16 28.6 7.6 28.6 2.4 23.2 2.4 15.2 2.4 9.6 5.8 6 10.4 4.3Z",
        "diamond": "M13.9 2.9Q16 .8 18.1 2.9L29.1 13.9Q31.2 16 29.1 18.1L18.1 29.1Q16 31.2 13.9 29.1L2.9 18.1Q.8 16 2.9 13.9Z",
        "shield": "M4 5.6Q4 3 6.6 3H25.4Q28 3 28 5.6V15C28 22.4 22.2 27.6 16 30.2 9.8 27.6 4 22.4 4 15Z",
        "bell": "M16 2.4C22 2.4 24.6 7.4 24.6 13V18.6C24.6 22.4 28.6 23.8 28.6 26.6 28.6 28.3 27.4 29 25.6 29H6.4C4.6 29 3.4 28.3 3.4 26.6 3.4 23.8 7.4 22.4 7.4 18.6V13C7.4 7.4 10 2.4 16 2.4Z",
    ]

    // EYE_CENTER.
    static let eyeCenters: [String: CGPoint] = [
        "circle": CGPoint(x: 16, y: 15), "squircle": CGPoint(x: 16, y: 15), "blob": CGPoint(x: 16.4, y: 15),
        "hexagon": CGPoint(x: 16, y: 15.2), "drop": CGPoint(x: 16, y: 18), "pill": CGPoint(x: 16, y: 16),
        "triangle": CGPoint(x: 16, y: 19.4), "cloud": CGPoint(x: 16.4, y: 19), "flower": CGPoint(x: 16, y: 15.4),
        "ghost": CGPoint(x: 16, y: 14), "bean": CGPoint(x: 16, y: 16), "diamond": CGPoint(x: 16, y: 15),
        "shield": CGPoint(x: 16, y: 13.6), "bell": CGPoint(x: 16, y: 12.6),
    ]

    // AVATAR_COLORS: hue, and the chroma at the dark and pale tones.
    private static let colors: [String: (hue: Double, dark: Double, pale: Double)] = [
        "peach": (56, 0.146, 0.102), "mauve": (313, 0.17, 0.11), "sage": (124, 0.148, 0.11),
        "sky": (203, 0.102, 0.11), "azure": (236, 0.128, 0.092), "rose": (345, 0.17, 0.11),
        "mint": (163, 0.128, 0.11), "lilac": (282, 0.17, 0.08),
    ]
    private static let renamedColors = ["sand": "sage"]

    static func shape(_ raw: String?) -> String {
        guard let raw, shapes.contains(raw) else { return "circle" }
        return raw
    }

    static func tone(_ raw: String?) -> String {
        guard let raw, ["deep", "dark", "pale"].contains(raw) else { return "deep" }
        return raw
    }

    // bodyOklch: L and C by tone, the colour's hue; an unknown colour is the
    // first one, as on the web.
    static func bodyOklch(color raw: String?, tone rawTone: String?) -> (l: Double, c: Double, h: Double) {
        let id = renamedColors[raw ?? ""] ?? raw ?? ""
        let entry = colors[id] ?? colors["peach"]!
        switch tone(rawTone) {
        case "dark": return (0.60, entry.dark, entry.hue)
        case "pale": return (0.84, entry.pale, entry.hue)
        default: return (0.68, 0.12, entry.hue)
        }
    }

    // oklch → sRGB components in 0...1, clipped per channel as a browser does.
    static func srgb(l: Double, c: Double, h: Double) -> (r: Double, g: Double, b: Double) {
        let rad = h * .pi / 180
        let a = c * cos(rad), b = c * sin(rad)
        let l_ = l + 0.3963377774 * a + 0.2158037573 * b
        let m_ = l - 0.1055613458 * a - 0.0638541728 * b
        let s_ = l - 0.0894841775 * a - 1.2914855480 * b
        let (L, M, S) = (l_ * l_ * l_, m_ * m_ * m_, s_ * s_ * s_)
        let lr = 4.0767416621 * L - 3.3077115913 * M + 0.2309699292 * S
        let lg = -1.2684380046 * L + 2.6097574011 * M - 0.3413193965 * S
        let lb = -0.0041960863 * L - 0.7034186147 * M + 1.7076147010 * S
        func gamma(_ x: Double) -> Double {
            let v = x <= 0.0031308 ? 12.92 * x : 1.055 * pow(x, 1 / 2.4) - 0.055
            return min(1, max(0, v))
        }
        return (gamma(lr), gamma(lg), gamma(lb))
    }

    static func bodyColor(_ avatar: CarPlayDirectory.Avatar?) -> UIColor {
        let o = bodyOklch(color: avatar?.color, tone: avatar?.tone)
        let c = srgb(l: o.l, c: o.c, h: o.h)
        return UIColor(red: c.r, green: c.g, blue: c.b, alpha: 1)
    }

    // path is the outline in the 32×32 box.
    static func path(_ rawShape: String?) -> UIBezierPath {
        let name = shape(rawShape)
        if name == "flower" { return flowerPath() }
        return SVGPath.parse(paths[name] ?? paths["circle"]!)
    }

    static func image(_ avatar: CarPlayDirectory.Avatar?, size: CGSize, scale: CGFloat, enabled: Bool = true) -> UIImage {
        let format = UIGraphicsImageRendererFormat()
        format.scale = scale
        format.opaque = false
        return UIGraphicsImageRenderer(size: size, format: format).image { context in
            let side = min(size.width, size.height)
            let cg = context.cgContext
            cg.translateBy(x: (size.width - side) / 2, y: (size.height - side) / 2)
            cg.scaleBy(x: side / 32, y: side / 32)
            let name = shape(avatar?.shape)
            let body = bodyColor(avatar).withAlphaComponent(enabled ? 1 : 0.4)
            body.setFill()
            path(name).fill()
            let eye = tone(avatar?.tone) == "pale" ? UIColor(red: 0x12 / 255, green: 0x12 / 255, blue: 0x1a / 255, alpha: 1) : .white
            eye.withAlphaComponent(enabled ? 1 : 0.6).setFill()
            let center = eyeCenters[name] ?? eyeCenters["circle"]!
            for side in [-1.0, 1.0] {
                let x = center.x + CGFloat(side) * 4.9
                UIBezierPath(ovalIn: CGRect(x: x - 2.2, y: center.y + 0.2 - 2.2, width: 4.4, height: 4.4)).fill()
            }
        }
    }

    private static func flowerPath() -> UIBezierPath {
        let path = UIBezierPath()
        for i in 0..<84 {
            let t = Double(i) / 84 * 2 * .pi
            let r = 12.6 + 1.9 * cos(7 * t)
            let p = CGPoint(x: 16 + r * sin(t), y: 16.4 - r * cos(t))
            if i == 0 { path.move(to: p) } else { path.addLine(to: p) }
        }
        path.close()
        return path
    }
}

// SVGPath reads the subset of SVG path data the avatar outlines use:
// M L H V C S Q T A Z, absolute and relative, with implicit repeats.
// Arcs are circular here (rx == ry, no rotation); an elliptical one would be
// drawn as a straight line.
enum SVGPath {
    static func parse(_ d: String) -> UIBezierPath {
        let path = UIBezierPath()
        var tokens = tokenize(d)[...]
        var current = CGPoint.zero
        var start = CGPoint.zero
        var lastControl: CGPoint?
        var lastCommand: Character = " "
        var command: Character = " "

        func number() -> CGFloat? {
            guard case .number(let n)? = tokens.first else { return nil }
            tokens.removeFirst()
            return CGFloat(n)
        }

        while !tokens.isEmpty {
            if case .command(let c)? = tokens.first {
                tokens.removeFirst()
                command = c
            } else if command == " " {
                break
            }
            let relative = command.isLowercase
            let base = relative ? current : .zero
            let upper = Character(command.uppercased())
            switch upper {
            case "M":
                guard let x = number(), let y = number() else { return path }
                current = CGPoint(x: base.x + x, y: base.y + y)
                start = current
                path.move(to: current)
                // Further pairs are lines.
                command = relative ? "l" : "L"
                lastControl = nil
            case "L":
                guard let x = number(), let y = number() else { return path }
                current = CGPoint(x: base.x + x, y: base.y + y)
                path.addLine(to: current)
                lastControl = nil
            case "H":
                guard let x = number() else { return path }
                current = CGPoint(x: (relative ? current.x : 0) + x, y: current.y)
                path.addLine(to: current)
                lastControl = nil
            case "V":
                guard let y = number() else { return path }
                current = CGPoint(x: current.x, y: (relative ? current.y : 0) + y)
                path.addLine(to: current)
                lastControl = nil
            case "C":
                guard let x1 = number(), let y1 = number(), let x2 = number(), let y2 = number(),
                      let x = number(), let y = number() else { return path }
                let c1 = CGPoint(x: base.x + x1, y: base.y + y1)
                let c2 = CGPoint(x: base.x + x2, y: base.y + y2)
                current = CGPoint(x: base.x + x, y: base.y + y)
                path.addCurve(to: current, controlPoint1: c1, controlPoint2: c2)
                lastControl = c2
            case "S":
                guard let x2 = number(), let y2 = number(), let x = number(), let y = number() else { return path }
                let c1 = "CS".contains(Character(lastCommand.uppercased())) ? reflect(lastControl, current) : current
                let c2 = CGPoint(x: base.x + x2, y: base.y + y2)
                current = CGPoint(x: base.x + x, y: base.y + y)
                path.addCurve(to: current, controlPoint1: c1, controlPoint2: c2)
                lastControl = c2
            case "Q":
                guard let x1 = number(), let y1 = number(), let x = number(), let y = number() else { return path }
                let c = CGPoint(x: base.x + x1, y: base.y + y1)
                current = CGPoint(x: base.x + x, y: base.y + y)
                path.addQuadCurve(to: current, controlPoint: c)
                lastControl = c
            case "T":
                guard let x = number(), let y = number() else { return path }
                let c = "QT".contains(Character(lastCommand.uppercased())) ? reflect(lastControl, current) : current
                current = CGPoint(x: base.x + x, y: base.y + y)
                path.addQuadCurve(to: current, controlPoint: c)
                lastControl = c
            case "A":
                guard let rx = number(), let ry = number(), let _ = number(), let large = number(), let sweep = number(),
                      let x = number(), let y = number() else { return path }
                let end = CGPoint(x: base.x + x, y: base.y + y)
                arc(path, from: current, to: end, rx: rx, ry: ry, large: large != 0, sweep: sweep != 0)
                current = end
                lastControl = nil
            case "Z":
                path.close()
                current = start
                lastControl = nil
                lastCommand = command
                // Numbers right after Z are not valid path data.
                command = " "
                continue
            default:
                return path
            }
            lastCommand = command
        }
        return path
    }

    private static func reflect(_ control: CGPoint?, _ point: CGPoint) -> CGPoint {
        guard let control else { return point }
        return CGPoint(x: 2 * point.x - control.x, y: 2 * point.y - control.y)
    }

    // SVG 1.1 F.6.5, for a circle: endpoint form to centre and angles.
    private static func arc(_ path: UIBezierPath, from p1: CGPoint, to p2: CGPoint, rx rawRx: CGFloat, ry rawRy: CGFloat,
                            large: Bool, sweep: Bool) {
        var r = abs(rawRx)
        guard r > 0, abs(abs(rawRx) - abs(rawRy)) < 0.0001, p1 != p2 else {
            path.addLine(to: p2)
            return
        }
        let x1 = (p1.x - p2.x) / 2, y1 = (p1.y - p2.y) / 2
        let lambda = (x1 * x1 + y1 * y1) / (r * r)
        if lambda > 1 { r *= sqrt(lambda) }
        let num = r * r * r * r - r * r * y1 * y1 - r * r * x1 * x1
        let den = r * r * y1 * y1 + r * r * x1 * x1
        let coef = (large != sweep ? 1 : -1) * sqrt(max(0, num / den))
        let cx1 = coef * y1, cy1 = coef * -x1
        let center = CGPoint(x: cx1 + (p1.x + p2.x) / 2, y: cy1 + (p1.y + p2.y) / 2)
        let a1 = atan2(y1 - cy1, x1 - cx1)
        let a2 = atan2(-y1 - cy1, -x1 - cx1)
        path.addArc(withCenter: center, radius: r, startAngle: a1, endAngle: a2, clockwise: sweep)
    }

    private enum Token {
        case command(Character)
        case number(Double)
    }

    private static func tokenize(_ d: String) -> [Token] {
        var out: [Token] = []
        var buffer = ""
        var seenDot = false
        func flush() {
            if !buffer.isEmpty, let n = Double(buffer) { out.append(.number(n)) }
            buffer = ""
            seenDot = false
        }
        for ch in d {
            if ch.isLetter && ch != "e" && ch != "E" {
                flush()
                out.append(.command(ch))
            } else if ch == "-" || ch == "+" {
                if let last = buffer.last, last == "e" || last == "E" {
                    buffer.append(ch)
                } else {
                    flush()
                    buffer.append(ch)
                }
            } else if ch == "." {
                if seenDot { flush() }
                buffer.append(ch)
                seenDot = true
            } else if ch.isNumber || ch == "e" || ch == "E" {
                buffer.append(ch)
            } else {
                flush()
            }
        }
        flush()
        return out
    }
}
