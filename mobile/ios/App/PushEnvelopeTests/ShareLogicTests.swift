import XCTest

final class ShareLogicTests: XCTestCase {
    func testComposedDeduplicatesOneLinePerValue() {
        XCTAssertEqual(
            ShareText.composed(title: "Page", text: "Read https://a.example/x", url: "https://a.example/x"),
            "Read https://a.example/x\nPage")
        XCTAssertEqual(ShareText.composed(title: "https://a.example", text: "", url: "https://a.example"), "https://a.example")
        XCTAssertEqual(ShareText.composed(title: " ", text: "\n", url: ""), "")
    }

    func testMessagePutsNoteFirstAndSkipsEmptyNote() {
        XCTAssertEqual(ShareText.message(note: " léelo ", title: "", text: "", url: "https://a.example"), "léelo\nhttps://a.example")
        XCTAssertEqual(ShareText.message(note: "  ", title: "", text: "", url: "https://a.example"), "https://a.example")
        XCTAssertEqual(ShareText.message(note: "hi", title: "", text: "", url: ""), "hi")
    }

    func testSummary() {
        XCTAssertEqual(ShareText.summary(fileNames: ["a.png"], title: "", text: "", url: ""), "a.png")
        XCTAssertEqual(ShareText.summary(fileNames: ["a.png", "b.pdf"], title: "", text: "", url: ""), "2 files · a.png, b.pdf")
        let long = String(repeating: "word ", count: 40)
        let summary = ShareText.summary(fileNames: [], title: "", text: long, url: "")
        XCTAssertEqual(summary.count, 80)
        XCTAssertTrue(summary.hasSuffix("…"))
    }

    func testDecodeIgnoresUnknownFieldsAndKindsAndUnsafeIDs() throws {
        let json = """
        {"extra":1,"destinations":[
          {"kind":"owner","session_id":"o1","title":"moa","project":"moa","state":"idle","updated":"x","avatar":"a"},
          {"kind":"session","session_id":"s-2","title":"","project":"","state":"saved"},
          {"kind":"hologram","session_id":"h1","title":"?"},
          {"kind":"session","session_id":"../x","title":"bad"},
          {"kind":"session","title":"no id"}
        ]}
        """
        let result = try ShareDestinations.decode(Data(json.utf8))
        XCTAssertEqual(result.map(\.sessionID), ["o1", "s-2"])
        XCTAssertEqual(result[0].kind, .owner)
        XCTAssertEqual(result[0].subtitle, "moa · working")
        XCTAssertEqual(result[1].displayTitle, "Untitled")
        XCTAssertEqual(result[1].subtitle, "closed")
    }

    func testDecodeRejectsWrongShape() {
        XCTAssertThrowsError(try ShareDestinations.decode(Data("[]".utf8)))
    }

    func testFilterMatchesTitleOrProjectCaseInsensitively() {
        let list = [
            ShareDestination(kind: .owner, sessionID: "1", title: "Winerim", project: "wine", state: "idle"),
            ShareDestination(kind: .session, sessionID: "2", title: "Fix", project: "API", state: "saved")
        ]
        XCTAssertEqual(ShareDestinations.filter(list, query: "").count, 2)
        XCTAssertEqual(ShareDestinations.filter(list, query: "WINE").map(\.sessionID), ["1"])
        XCTAssertEqual(ShareDestinations.filter(list, query: "api").map(\.sessionID), ["2"])
        XCTAssertTrue(ShareDestinations.filter(list, query: "zzz").isEmpty)
    }
}
