import XCTest

final class ShareBodyWriterTests: XCTestCase {
    private var dir: URL!

    override func setUpWithError() throws {
        dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
    }

    override func tearDown() {
        try? FileManager.default.removeItem(at: dir)
    }

    private func file(_ bytes: Data) throws -> URL {
        let url = dir.appendingPathComponent(UUID().uuidString)
        try bytes.write(to: url)
        return url
    }

    private func decode(_ body: URL) throws -> (text: String, files: [[String: String]]) {
        let object = try JSONSerialization.jsonObject(with: Data(contentsOf: body)) as! [String: Any]
        return (object["text"] as! String, object["attachments"] as! [[String: String]])
    }

    func testBase64DecodesToExactBytesForAwkwardSizes() throws {
        // chunkSize 12 forces many chunks; sizes cover 0, <chunk, multiples and non-multiples of 3.
        for size in [0, 1, 2, 3, 11, 12, 13, 25, 36, 1000] {
            let bytes = Data((0..<size).map { UInt8(truncatingIfNeeded: $0 &* 7 &+ 3) })
            let out = dir.appendingPathComponent("body-\(size).json")
            try ShareBodyWriter.write(
                text: "t",
                attachments: [ShareAttachment(name: "f.bin", mime: "application/octet-stream", url: try file(bytes))],
                to: out, chunkSize: 12)
            let decoded = try decode(out)
            XCTAssertEqual(Data(base64Encoded: decoded.files[0]["data"]!), bytes, "size \(size)")
            XCTAssertEqual(decoded.files[0]["name"], "f.bin")
            XCTAssertEqual(decoded.files[0]["mime"], "application/octet-stream")
        }
    }

    func testDefaultChunkSizeLargeFile() throws {
        let bytes = Data((0..<(ShareBodyWriter.chunkSize * 2 + 5)).map { UInt8(truncatingIfNeeded: $0) })
        let out = dir.appendingPathComponent("big.json")
        try ShareBodyWriter.write(text: "", attachments: [
            ShareAttachment(name: "a", mime: "x/y", url: try file(bytes))], to: out)
        XCTAssertEqual(Data(base64Encoded: try decode(out).files[0]["data"]!), bytes)
    }

    func testEmptyNoteNoAttachmentsAndEscaping() throws {
        let out = dir.appendingPathComponent("t.json")
        try ShareBodyWriter.write(text: "", attachments: [], to: out)
        var decoded = try decode(out)
        XCTAssertEqual(decoded.text, "")
        XCTAssertTrue(decoded.files.isEmpty)

        let tricky = "quote \" slash \\ newline\n tab\t unicode é 😀"
        try ShareBodyWriter.write(text: tricky, attachments: [
            ShareAttachment(name: "n\"ame.txt", mime: "text/plain", url: try file(Data("hi".utf8))),
            ShareAttachment(name: "b", mime: "text/plain", url: try file(Data()))], to: out)
        decoded = try decode(out)
        XCTAssertEqual(decoded.text, tricky)
        XCTAssertEqual(decoded.files.map { $0["name"] }, ["n\"ame.txt", "b"])
        XCTAssertEqual(decoded.files[1]["data"], "")
    }
}
