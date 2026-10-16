import UIKit
import XCTest

final class CarPlayOwnersTests: XCTestCase {
    private func directory(_ owners: [(String, String, String?)]) -> CarPlayDirectory {
        CarPlayDirectory(owners: owners.map { id, state, shape in
            CarPlayDirectory.Owner(id: id, name: "Owner \(id)", avatar: .init(shape: shape, color: "sky", tone: nil), state: state)
        })
    }

    // MARK: List

    func testRowsKeepEveryOwnerInTheAPIOrder() {
        let rows = CarPlayListModel.rows(directory([("c", "open", nil), ("a", "closed", nil), ("b", "none", nil)]), limit: 12)
        XCTAssertEqual(rows.map(\.id), ["c", "a", "b"])
    }

    func testStatesBecomeSubtitleAndEnabled() {
        let rows = CarPlayListModel.rows(directory([("o", "open", nil), ("c", "closed", nil), ("n", "none", nil), ("x", "weird", nil)]), limit: 12)
        XCTAssertEqual(rows[0].subtitle, nil)
        XCTAssertTrue(rows[0].enabled)
        XCTAssertEqual(rows[1].subtitle, "Closed")
        XCTAssertTrue(rows[1].enabled, "a closed owner is called like an open one")
        XCTAssertEqual(rows[2].subtitle, nil)
        XCTAssertFalse(rows[2].enabled)
        XCTAssertFalse(rows[3].enabled, "an unknown state is not callable")
    }

    func testRowsStopAtTheCarLimit() {
        let owners = (0..<20).map { ("o\($0)", "open", nil as String?) }
        let rows = CarPlayListModel.rows(directory(owners), limit: 12)
        XCTAssertEqual(rows.count, 12)
        XCTAssertEqual(rows.last?.id, "o11")
        XCTAssertEqual(CarPlayListModel.rows(directory(owners), limit: 0).count, 0)
    }

    func testContentFromTheDirectoryResponse() {
        let body = Data(#"{"owners":[{"id":"a","name":"A","avatar":{"shape":"bell","color":"rose","tone":"pale"},"state":"open"},{"id":"b","name":"B","avatar":{"shape":"x","color":"y"},"state":"closed"}]}"#.utf8)
        guard case .owners(let rows) = CarPlayListModel.content(hasCredential: true, status: 200, data: body, limit: 12) else {
            return XCTFail("expected owners")
        }
        XCTAssertEqual(rows.map(\.name), ["A", "B"])
        XCTAssertEqual(rows[0].avatar?.tone, "pale")
        XCTAssertEqual(rows[1].subtitle, "Closed")
    }

    func testContentWithoutCredentialOrServer() {
        XCTAssertEqual(CarPlayListModel.content(hasCredential: false, status: nil, data: nil, limit: 12),
                       .message("moa isn't set up for calls yet"))
        XCTAssertEqual(CarPlayListModel.content(hasCredential: true, status: 401, data: nil, limit: 12),
                       .message("moa isn't set up for calls yet"))
        XCTAssertEqual(CarPlayListModel.content(hasCredential: true, status: nil, data: nil, limit: 12),
                       .message("Can't reach your moa server"))
        XCTAssertEqual(CarPlayListModel.content(hasCredential: true, status: 500, data: nil, limit: 12),
                       .message("Can't reach your moa server"))
        XCTAssertEqual(CarPlayListModel.content(hasCredential: true, status: 200, data: Data("nope".utf8), limit: 12),
                       .message("Can't reach your moa server"))
    }

    func testNoTextAsksToPickUpTheIPhone() {
        let texts = [CarPlayText.notSetUp, CarPlayText.noPlan, CarPlayText.callActive, CarPlayText.unreachable, CarPlayText.micOff]
        for text in texts {
            XCTAssertFalse(text.lowercased().contains("iphone"), text)
            XCTAssertFalse(text.lowercased().contains("pick up"), text)
        }
    }

    // MARK: Creating the call

    private func json(_ object: [String: Any]) -> Data { try! JSONSerialization.data(withJSONObject: object) }

    func testCallStartOutcomes() {
        XCTAssertEqual(CarPlayCallStartModel.outcome(status: 201, data: json(["call_id": "c1", "ws_url": "/api/voice/calls/c1/ws"])),
                       .started(wsPath: "/api/voice/calls/c1/ws"))
        XCTAssertEqual(CarPlayCallStartModel.outcome(status: 201, data: json(["ws_url": "https://evil/ws"])), .alert(.unreachable))
        XCTAssertEqual(CarPlayCallStartModel.outcome(status: 409, data: json(["error": "call_active"])), .alert(.callActive))
        XCTAssertEqual(CarPlayCallStartModel.outcome(status: 409, data: json(["error": "owner_busy", "retryable": true])),
                       .alert(.unreachable))
        XCTAssertEqual(CarPlayCallStartModel.outcome(status: 503, data: json(["error": "plan_unavailable", "reason": "no_plan",
                                                                               "paid_option": ["provider": "xai"]])), .alert(.noPlan))
        XCTAssertEqual(CarPlayCallStartModel.outcome(status: 503, data: json(["error": "no_plan"])), .alert(.noPlan))
        XCTAssertEqual(CarPlayCallStartModel.outcome(status: 502, data: json(["error": "provider_unavailable"])), .alert(.noPlan))
        XCTAssertEqual(CarPlayCallStartModel.outcome(status: 503, data: json(["error": "shutting_down"])), .alert(.unreachable))
        XCTAssertEqual(CarPlayCallStartModel.outcome(status: 429, data: json(["error": "rate_limited"])), .alert(.unreachable))
        XCTAssertEqual(CarPlayCallStartModel.outcome(status: 404, data: json(["error": "owner_not_found"])), .reload)
        XCTAssertEqual(CarPlayCallStartModel.outcome(status: 409, data: json(["error": "owner_has_no_conversation"])), .reload)
        XCTAssertEqual(CarPlayCallStartModel.outcome(status: 401, data: nil), .reload)
        XCTAssertEqual(CarPlayCallStartModel.outcome(status: nil, data: nil), .alert(.unreachable))
    }

    func testThePostCarriesTheCSRFMarkerAndTheVoiceCredential() throws {
        let client = CarPlayClient(origin: "https://moa.example", authorization: "Moa-Voice d.k")
        let post = try XCTUnwrap(client.makeRequest("POST", "/api/voice/owners/o1/call", body: Data("{}".utf8)))
        XCTAssertEqual(post.url?.absoluteString, "https://moa.example/api/voice/owners/o1/call")
        XCTAssertEqual(post.httpMethod, "POST")
        XCTAssertEqual(post.value(forHTTPHeaderField: "X-Moa-Request"), "1")
        XCTAssertEqual(post.value(forHTTPHeaderField: "Authorization"), "Moa-Voice d.k")
        XCTAssertEqual(post.value(forHTTPHeaderField: "Content-Type"), "application/json")
        let get = try XCTUnwrap(client.makeRequest("GET", "/api/voice/directory", body: nil))
        XCTAssertEqual(get.value(forHTTPHeaderField: "Authorization"), "Moa-Voice d.k")
        XCTAssertNil(CarPlayClient(origin: "http://moa.example", authorization: "x").makeRequest("POST", "/a", body: nil))
    }

    // MARK: Call events → voice state

    func testEventsDriveTheVoiceStates() {
        var m = CarPlayCallMachine()
        XCTAssertEqual(m.phase, .connecting)
        XCTAssertEqual(m.apply(.playback("playing")), nil, "nothing shows before the socket opens")
        XCTAssertEqual(m.apply(.playback("drained")), nil)
        XCTAssertEqual(m.apply(.open), .show(.listening))
        XCTAssertEqual(m.apply(.playback("playing")), .show(.speaking))
        XCTAssertEqual(m.apply(.playback("playing")), nil)
        XCTAssertEqual(m.apply(.playback("drained")), .show(.listening))
        XCTAssertEqual(m.apply(.mute(true)), .show(.muted))
        XCTAssertEqual(m.apply(.playback("playing")), .show(.speaking), "the agent is heard while muted")
        XCTAssertEqual(m.apply(.playback("drained")), .show(.muted))
        XCTAssertEqual(m.apply(.mute(false)), .show(.listening))
    }

    func testCloseEndsWithAnAlertOnlyWhenUnexpected() {
        var user = CarPlayCallMachine()
        _ = user.apply(.open)
        XCTAssertEqual(user.apply(.close(reason: "native:native_hangup_timeout", serverEnded: false, userEnded: true)), .end(alert: nil))
        XCTAssertEqual(user.apply(.open), nil, "nothing after the end")

        var server = CarPlayCallMachine()
        _ = server.apply(.open)
        XCTAssertEqual(server.apply(.close(reason: "native:server_closed 1000", serverEnded: true, userEnded: false)), .end(alert: nil))

        var dropped = CarPlayCallMachine()
        _ = dropped.apply(.open)
        XCTAssertEqual(dropped.apply(.close(reason: "native:socket NSURLErrorDomain:-1005", serverEnded: false, userEnded: false)),
                       .end(alert: .unreachable))

        var neverOpened = CarPlayCallMachine()
        XCTAssertEqual(neverOpened.apply(.close(reason: "native:receive x", serverEnded: false, userEnded: false)), .end(alert: .unreachable))

        var mic = CarPlayCallMachine()
        XCTAssertEqual(mic.apply(.close(reason: "native:mic_not_granted mic=denied", serverEnded: false, userEnded: false)),
                       .end(alert: .micOff))

        var phone = CarPlayCallMachine()
        _ = phone.apply(.open)
        XCTAssertEqual(phone.apply(.close(reason: "native:interrupted mic=granted", serverEnded: false, userEnded: false)),
                       .end(alert: nil))
    }

    // MARK: Avatars

    func testEveryShapeHasAnOutlineInsideTheBox() {
        for shape in CarPlayAvatar.shapes {
            let bounds = CarPlayAvatar.path(shape).bounds
            XCTAssertFalse(bounds.isEmpty, shape)
            XCTAssertGreaterThan(bounds.width, 16, shape)
            XCTAssertGreaterThan(bounds.height, 16, shape)
            XCTAssertGreaterThanOrEqual(bounds.minX, -0.5, shape)
            XCTAssertGreaterThanOrEqual(bounds.minY, -0.5, shape)
            XCTAssertLessThanOrEqual(bounds.maxX, 32.5, shape)
            XCTAssertLessThanOrEqual(bounds.maxY, 32.5, shape)
        }
    }

    func testCircleArcsMakeTheWebCircle() {
        let bounds = CarPlayAvatar.path("circle").bounds
        XCTAssertEqual(bounds.minX, 1.5, accuracy: 0.05)
        XCTAssertEqual(bounds.minY, 1.5, accuracy: 0.05)
        XCTAssertEqual(bounds.width, 29, accuracy: 0.05)
        XCTAssertEqual(bounds.height, 29, accuracy: 0.05)
        XCTAssertTrue(CarPlayAvatar.path("circle").contains(CGPoint(x: 16, y: 16)))
        let pill = CarPlayAvatar.path("pill").bounds
        XCTAssertEqual(pill.minX, 1.5, accuracy: 0.05)
        XCTAssertEqual(pill.maxX, 30.5, accuracy: 0.05)
        XCTAssertEqual(pill.minY, 6, accuracy: 0.05)
        XCTAssertEqual(pill.maxY, 26, accuracy: 0.05)
    }

    func testUnknownShapeFallsBackToCircle() {
        XCTAssertEqual(CarPlayAvatar.shape("star"), "circle")
        XCTAssertEqual(CarPlayAvatar.shape(nil), "circle")
        XCTAssertEqual(CarPlayAvatar.path("star").bounds, CarPlayAvatar.path("circle").bounds)
        XCTAssertEqual(CarPlayAvatar.shape("ghost"), "ghost")
    }

    func testBodyColourFollowsToneAndFallsBack() {
        let deep = CarPlayAvatar.bodyOklch(color: "sky", tone: nil)
        XCTAssertEqual(deep.l, 0.68)
        XCTAssertEqual(deep.c, 0.12)
        XCTAssertEqual(deep.h, 203)
        XCTAssertEqual(CarPlayAvatar.bodyOklch(color: "sky", tone: "pale").l, 0.84)
        XCTAssertEqual(CarPlayAvatar.bodyOklch(color: "mauve", tone: "dark").c, 0.17)
        XCTAssertEqual(CarPlayAvatar.bodyOklch(color: "sand", tone: nil).h, 124, "sand was renamed sage")
        XCTAssertEqual(CarPlayAvatar.bodyOklch(color: "nope", tone: "nope").h, 56, "unknown colour is peach, unknown tone is deep")
        // The palette's own sRGB for peach at L 0.80, C 0.115: #f6aa73.
        let peach = CarPlayAvatar.srgb(l: 0.80, c: 0.115, h: 56)
        XCTAssertEqual(peach.r * 255, 0xf6, accuracy: 2)
        XCTAssertEqual(peach.g * 255, 0xaa, accuracy: 2)
        XCTAssertEqual(peach.b * 255, 0x73, accuracy: 2)
    }

    func testAvatarImageHasTheAskedSize() {
        let image = CarPlayAvatar.image(.init(shape: "hexagon", color: "mint", tone: "dark"), size: CGSize(width: 44, height: 44), scale: 2)
        XCTAssertEqual(image.size, CGSize(width: 44, height: 44))
        XCTAssertEqual(image.scale, 2)
    }
}
