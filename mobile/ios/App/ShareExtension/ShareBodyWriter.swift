import Foundation

struct ShareAttachment {
    let name: String
    let mime: String
    let url: URL
}

// Writes the /send JSON body to a file, base64-encoding attachments in
// chunks. A share extension has roughly 120 MB; 32 MB of files as one base64
// string (plus its JSON copy) would not fit, so nothing here is whole-file.
enum ShareBodyWriter {
    // Must be a multiple of 3 so chunk encodings concatenate without padding.
    static let chunkSize = 3 * 64 * 1024

    static func write(
        text: String,
        attachments: [ShareAttachment],
        to destination: URL,
        chunkSize: Int = ShareBodyWriter.chunkSize
    ) throws {
        precondition(chunkSize > 0 && chunkSize % 3 == 0)
        FileManager.default.createFile(atPath: destination.path, contents: nil)
        let output = try FileHandle(forWritingTo: destination)
        defer { try? output.close() }

        try output.write(contentsOf: Data("{\"text\":\(try literal(text)),\"attachments\":[".utf8))
        for (index, attachment) in attachments.enumerated() {
            let head = "\(index == 0 ? "" : ","){\"name\":\(try literal(attachment.name)),"
                + "\"mime\":\(try literal(attachment.mime)),\"data\":\""
            try output.write(contentsOf: Data(head.utf8))
            let input = try FileHandle(forReadingFrom: attachment.url)
            defer { try? input.close() }
            while true {
                let chunk = try readFull(input, count: chunkSize)
                if chunk.isEmpty { break }
                try output.write(contentsOf: chunk.base64EncodedData())
                if chunk.count < chunkSize { break }
            }
            try output.write(contentsOf: Data("\"}".utf8))
        }
        try output.write(contentsOf: Data("]}".utf8))
    }

    private static func readFull(_ handle: FileHandle, count: Int) throws -> Data {
        var data = Data()
        while data.count < count {
            guard let part = try handle.read(upToCount: count - data.count), !part.isEmpty else { break }
            data.append(part)
        }
        return data
    }

    private static func literal(_ value: String) throws -> String {
        let data = try JSONSerialization.data(withJSONObject: value, options: .fragmentsAllowed)
        return String(decoding: data, as: UTF8.self)
    }
}
