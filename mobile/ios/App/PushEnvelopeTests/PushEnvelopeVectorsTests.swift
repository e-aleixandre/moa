import CryptoKit
import XCTest

// Pins the Swift side of moa native push to relay/test/vectors.json, which
// the Go server and the relay also reproduce. The values are copied, not
// read, so this target needs no resources; regenerate both together.
final class PushEnvelopeVectorsTests: XCTestCase {
    private let secret = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8" // ggignore: public test vector (bytes 0..31), not a credential
    private let kEncHex = "ea2892f9fc9ff60ecd5fd1bc92e5a6945f1d426752bbfba773c60bcc122f6cf8"
    private let kSendHex = "84e7dbb913ee47c77b4894981a986ab0df476340f444dba2ba2b50251a8f6773"
    private let kColHex = "967e8a69c9d1485949b39bf115ae9cb0085dfba346f0c4743d5c8a276f917634"
    private let kidHex = "6e0a5ea0eb068f24"

    private let now = Date(timeIntervalSince1970: 1790000000)
    private let expiry: TimeInterval = 1790043200
    private let envelopeKid = "bgpeoOsGjyQ"
    private let envelopeCiphertext = [
        "gIGCg4SFhoeIiYqLZzpKN4UIB2JfE9tO4e_DVrMM5b56Nnm-x9bNRYiHH2_psAliZmNIVXyqMIllx9kb4Hk0O1GCNrt28dAl",
        "NQrR_oUcTMISCq-wDpMsavKdIeRt-ia-qCUAgFzZEAAkOQGc1pdSD6Y3a-30zDtLEKXBaVUK8ZwncnKm_vyQNLgg7g4XDeEf",
        "NvMrW57-kWR2N_YSg0ATtk6nyIOva1WUbe1ci6KAAAl_w0dbVS-3q3LYDk1WVn9ZmSijP5to6CO4u3SJT6PQcZikrFEbRqZR",
        "Kyn6Vej-gmVHchNQTUE14Mf2sFdaNs7XZsbMptPTBMDqLSTquYotVCN95pIEJ69yxnzOYsF3KJgE7gnifcXFnO-tgrDnL3Ig",
        "lB1VRePjbH_mN61ec8yKrdOKCkXGupCbQSlNPpPZDaVpG4UNFq6fhXuSVCaTGNSKjsNZgNAbnIV7LFc_fAdB4TCNk-diuF1N",
        "GrJb4YL84XCGDZXZ2-YaH-yNckZ52rliSgHwSwd5XTvxuKAL_tp5rmXZ2AL67QNEsSGJEAgKyy8r1zXb9lo1-kg8AE4OCogq",
        "7TXIu7Y7NDon3SrmX6kYnEVs2nwc2yqzX9UC0Qc4CQCHZv6AsDOvVaTPGRRhoiDxCzLf2_Hv7eOb5hN89sb_FMCDiVIN20w5",
        "jzIWwOG_FrjtQTi5MAStUzUiVN05exmVk7rYhmYLGxUPx2tkhm9ozLSHFOJ156N1jffrdHYPeiHEBAa-qG-Sp25znOWaVCKN",
        "0mHZMFZ7Tom-WextsuXs1bRLX__uHiwfFRscepjQssF8nZklGn5mqDB_wHqYX6YEfs9rmJ_8tUt_yqDaQh2Yity8_RHNCoco",
        "Z_W-xnDPDNdsTuacvXX_6770l9vGvmCCsOQ-fPSkJeQ0IUjYhi1B02axOekxnlrd7Rez5YoGYInI3O48c0CokvCgmJYaYeYl",
        "H9f35vQ_DgdJsbc8sipR7Twv-qYlnvTZrZnlD0XboxpxvYcLwSH6S7HFIUR_CYIgs-awL49TpKjLODQqOvsv_Wyjw37XCxY3",
        "Yna9cKdTT9lMH00x-bmjAreA13eaNa1Ou99Q6AAaDQHmtiwx-hmJrW5A476E_KKB5sgMpI8KCSVrnTHJXYF9xqrVEoqwLvLf",
        "cfJ7ABwbWjmrzIwg7faNwTpl9ui_D4-gEE9EQeoX9AoAic0aE6yu7wJAlJ12htx4aFVdoQeI5BZKEm2C0MZFfu_0neqty4KF",
        "urwmKKSdluTTVIuW_Hb9zXV81womJ7jMzc4x8Y7499Bh6FB4Dap36BGMpIT3VMB8oGv7yAxcBUELTQFKAE1Uxem4Pmg0ZoQo",
        "aTJsRq0k-VA9_l3XyGcGovUVqtkuSQJgnDpdyohIuFQukcHI4u-hyLCqFHi_4EZK0YHK0l8M7kesLqn3zogxftq0U_FrFEPS",
        "Kly83QD681LucPhOxecsRjeL7iKPeB5KbT-MGI2x9bgQ63KbX6BGGhYa_lLZYHBgwugU7TvZrD7WXYR6fhCEKjRDTwOOfah2",
        "sSnEl3QdTVOw6zKMZNqYrD_2rCp60HSzsPxK7jtF6GVFp9sJky4lbkE2guqdjGfoiEYsZQv_vUxl1Lf6MjJeM1l36vR8V-J0",
        "eMOLm9a4DIA0r2DYN6EM5wZs_ZC6FxsrrFAdESxFNvKt55lAmSweD1U2gJ9MlFbATGrVuGJdjGa0l5JW3KBWSPJyFhyxIXLX",
        "7xf1D637ueAXOxDrHFybHDvvC2bxfmZtNhxECDS1mTGDvWoq_Qc_zBuWAPDABRubR_PpwDdNBhDgmFSgrYZsozynStot5swJ",
        "y6M3d16joInYvA7GelJplYJCE0lfx0_l8TrnWfFdVkNHPmoHK1Av4AhtuHV9VFGmbQ9oOnZR_g0f6tt1ckQG8LUG5bWY98Vb",
        "uW-aOpHYeiLJoOgh0Sm42vXlI-wna_CkrtEqS2CTtzBQ8eOqxhhfjWS3bkHU72-2pz-xl44cuYcJIYMXIbtFiFIHwicQb-Bq",
        "-cpTVIKWc40Z_6Tgefl9abJfPOf362KcRdLKzozsSCDtcceSFgRVEuUcd_Z0lmEtR3nV5N7exGtAy9pT9GMx9sXLcks1WVmE",
        "OFC10ZaSWeMdRjASZDVytDEadzBSEfZ-yaxp44E6lp-CSC9WBCq2V52grxUzsjx6v4j4imKaHcMxnCAW--WUOwpEUMIGiTNm",
        "574_sHtz2mELvw1Oc2vLuz2a3rsNl7f143iwMmY4SQkwFoAJcgQJTF3wUwlto43zpHRdaXbu_jbhTMd1MoFlkx8c0RRw2mhK",
        "JOUyovgvkbVbnOR3UESsu3benrslRBXImIDSTJ1cfSIMWcIjSQdanFZ18g2ySOqev2T67y4t8oOW2CcnnEW6D6Qie3eXqVCj",
        "S0A5XrHZ7EiN8yBdzrhqkTnoUUmhPPrJtdDhKuZliwu8l5ifFhOpF1K_9eRIdjQdp0ALX0gjDJaU4RSehU2M9_jFPy7E3TzJ",
        "WTKIg_83isFN0GlPVKkaNegrPjTXATIuzVcEYZ2h2bMEH6BMSU3vlo13s7axczworCv9ZXRQSCXbyv3xDgHO1F5jS0B3xrth",
        "YEBfgC35-lGVaUlNvvZkiRlSFpaKvx8TzuqUEuRU0Zc4feqOMeZxbghj7aksUHnTGwxoaoHMSCDe-8-pKSPWx2Cw1Zj3qiu2",
        "hMsKd7-QsOD5-t_tASwUQ2vtzJf3sDHlYYVWBVXEatZlURvugKRNfH-ByehdKNTsQrv55sL3r1pBrrlW",
    ].joined()

    private let challenge = "AcDBwsPExcbHyMnKy8rFU1mHq0ERUkVTpCHqcuJqKCry-Qs1vBPVijqLcezgo5sFpe8SsdBykWm5D27yf5zQRTNh6Vfm6ITDXlN1eNSaG2SqvPcoQtThNQJD_cSYYYKy0AdD0GIuMBdLuDiXnFbKqrwWUSKgFpWy5I1lqE7mnIDha-uTJVek06lY_2T_OeeuFFPENMPecjR1csQg0qTHMUPeyb5TouZkvGD5DTOpYMYzMYVy"
    private let proof = "WQs5RD--DW8YXiJsn6wmoEto_E2tYl-5p5ljg0HRFMI"

    private var keys: PushEnvelope.Keys {
        PushEnvelope.deriveKeys(secret: PushEnvelope.base64URLDecode(secret)!)!
    }

    private var envelope: [String: Any] {
        ["v": 1, "k": envelopeKid, "c": envelopeCiphertext]
    }

    func testDerivesDeviceKeys() {
        let s = PushEnvelope.base64URLDecode(secret)!
        XCTAssertEqual(s.count, 32)
        XCTAssertEqual(hex(keys.enc.withUnsafeBytes { Data($0) }), kEncHex)
        XCTAssertEqual(hex(keys.send.withUnsafeBytes { Data($0) }), kSendHex)
        XCTAssertEqual(hex(keys.kid), kidHex)
        XCTAssertEqual(hex(PushEnvelope.derive(secret: s, label: "moa-push-collapse-v1", length: 32)), kColHex)
        XCTAssertEqual(PushEnvelope.base64URLEncode(keys.kid), envelopeKid)
    }

    func testRejectsSecretOfWrongLength() {
        XCTAssertNil(PushEnvelope.deriveKeys(secret: Data(repeating: 1, count: 31)))
    }

    func testOpensVectorEnvelope() throws {
        XCTAssertEqual(envelopeCiphertext.count, PushEnvelope.encodedCiphertextLength)
        let content = try XCTUnwrap(PushEnvelope.open(envelope, keys: keys, now: now))
        XCTAssertEqual(content.title, "moa necesita tu decisión")
        XCTAssertEqual(content.body, "Revisar «push» <nativo> & más")
        XCTAssertEqual(content.thread, "21zyv1iZL0j8IImOHR-zEL")
        XCTAssertEqual(content.destination, .session("sess_1"))
        XCTAssertEqual(content.level, .urgent)
        XCTAssertEqual(
            content.destination.url(origin: URL(string: "https://moa.example:8443")!)?.absoluteString,
            "https://moa.example:8443/?session=sess_1"
        )
    }

    func testConfirmProof() {
        XCTAssertEqual(PushEnvelope.confirmProof(challenge: challenge, sendKey: keys.send), proof)
    }

    func testRejectsTamperedCiphertext() {
        var tampered = envelope
        tampered["c"] = flipCharacter(in: envelopeCiphertext, at: 100)
        XCTAssertNil(PushEnvelope.open(tampered, keys: keys, now: now))
        tampered["c"] = flipCharacter(in: envelopeCiphertext, at: 5)
        XCTAssertNil(PushEnvelope.open(tampered, keys: keys, now: now), "nonce")
        tampered["c"] = flipCharacter(in: envelopeCiphertext, at: envelopeCiphertext.count - 3)
        XCTAssertNil(PushEnvelope.open(tampered, keys: keys, now: now), "tag")
    }

    func testRejectsWrongKid() {
        var wrong = envelope
        wrong["k"] = PushEnvelope.base64URLEncode(Data(repeating: 0, count: 8))
        XCTAssertNil(PushEnvelope.open(wrong, keys: keys, now: now))
    }

    func testRejectsAnotherDevicesKey() {
        let other = PushEnvelope.deriveKeys(secret: Data(repeating: 7, count: 32))!
        XCTAssertNil(PushEnvelope.open(envelope, keys: other, now: now))
        // Even with the right kid the tag fails under another K_enc.
        let forged = PushEnvelope.Keys(enc: other.enc, send: other.send, kid: keys.kid)
        XCTAssertNil(PushEnvelope.open(envelope, keys: forged, now: now))
    }

    func testRejectsExpired() {
        XCTAssertNotNil(PushEnvelope.open(envelope, keys: keys, now: Date(timeIntervalSince1970: expiry - 1)))
        XCTAssertNil(PushEnvelope.open(envelope, keys: keys, now: Date(timeIntervalSince1970: expiry)))
    }

    func testRejectsMalformedEnvelopes() {
        var outer = envelope
        outer["v"] = 2
        XCTAssertNil(PushEnvelope.open(outer, keys: keys, now: now), "outer version")
        outer = envelope
        outer["c"] = String(envelopeCiphertext.dropLast(4))
        XCTAssertNil(PushEnvelope.open(outer, keys: keys, now: now), "length")
        outer = envelope
        outer["c"] = envelopeCiphertext.replacingOccurrences(of: "-", with: "+")
        XCTAssertNil(PushEnvelope.open(outer, keys: keys, now: now), "alphabet")
        XCTAssertNil(PushEnvelope.open(nil, keys: keys, now: now))
        XCTAssertNil(PushEnvelope.open("not a dictionary", keys: keys, now: now))
    }

    // The cases below need plaintexts the vectors do not carry, so they are
    // sealed here with the vector key exactly as the server would.

    func testRejectsUnknownInnerVersion() {
        let sealed = seal(#"{"v":2,"id":"x","exp":1790043200,"d":"home","k":"done","lvl":"active","th":"t","t":"a","b":"b"}"#)
        XCTAssertNil(PushEnvelope.open(sealed, keys: keys, now: now))
    }

    func testRejectsUnpaddedPlaintext() {
        let sealed = seal(#"{"v":1,"id":"x","exp":1790043200,"d":"home","k":"done","lvl":"active","th":"t","t":"a","b":"b"}"#, padTo: 2047)
        XCTAssertNil(PushEnvelope.open(sealed, keys: keys, now: now))
    }

    func testInvalidSessionIdBecomesHome() throws {
        let sealed = seal(#"{"v":1,"id":"x","exp":1790043200,"d":"session","s":"../evil?x=1","k":"ask","lvl":"passive","th":"t","t":"a","b":"b"}"#)
        let content = try XCTUnwrap(PushEnvelope.open(sealed, keys: keys, now: now))
        XCTAssertEqual(content.destination, .home)
        XCTAssertEqual(content.level, .passive)
    }

    func testInboxDestination() throws {
        let sealed = seal(#"{"v":1,"id":"x","exp":1790043200,"d":"inbox","k":"event","lvl":"passive","th":"t","t":"a","b":"b"}"#)
        let content = try XCTUnwrap(PushEnvelope.open(sealed, keys: keys, now: now))
        XCTAssertEqual(content.destination, .inbox)
        XCTAssertEqual(
            content.destination.url(origin: URL(string: "https://moa.example")!)?.absoluteString,
            "https://moa.example/?inbox=1"
        )
    }

    private func seal(_ json: String, padTo size: Int = PushEnvelope.paddedSize) -> [String: Any] {
        var plaintext = Data(json.utf8)
        plaintext.append(Data(repeating: 0x20, count: size - plaintext.count))
        let box = try! AES.GCM.seal(
            plaintext,
            using: keys.enc,
            nonce: AES.GCM.Nonce(),
            authenticating: Data("moa-push-v1".utf8) + keys.kid
        )
        let raw = Data(box.nonce) + box.ciphertext + box.tag
        return ["v": 1, "k": envelopeKid, "c": PushEnvelope.base64URLEncode(raw)]
    }

    private func flipCharacter(in text: String, at offset: Int) -> String {
        var characters = Array(text)
        characters[offset] = characters[offset] == "A" ? "B" : "A"
        return String(characters)
    }

    private func hex(_ data: Data) -> String {
        data.map { String(format: "%02x", $0) }.joined()
    }
}

// The registration fencing primitives (review ec9c51f7 I1, I2). The flows
// that use them need UIKit, APNs and the Keychain and are covered by the
// manual QA in SPEC.md.
final class NativePushCoordinationTests: XCTestCase {
    private final class Log {
        var entries: [String] = []
        var release: CheckedContinuation<Void, Never>?
    }

    private enum Failure: Error { case expected }

    // A DELETE enqueued while an enable is suspended runs after it, and an
    // enable enqueued after that DELETE runs after the DELETE.
    @MainActor
    func testOperationsRunInEnqueueOrder() async {
        let queue = NativePushOperationQueue()
        let log = Log()
        let first = queue.enqueue {
            await withCheckedContinuation { log.release = $0 }
            log.entries.append("first")
        }
        let second = queue.enqueue { log.entries.append("second") }
        let third = queue.enqueue { log.entries.append("third") }
        while log.release == nil { await Task.yield() }
        XCTAssertEqual(log.entries, [])
        log.release?.resume()
        _ = await first.result
        _ = await second.result
        _ = await third.result
        XCTAssertEqual(log.entries, ["first", "second", "third"])
    }

    @MainActor
    func testFailedOperationDoesNotBlockTheNext() async {
        let queue = NativePushOperationQueue()
        let log = Log()
        let failing = queue.enqueue { throw Failure.expected }
        let next = queue.enqueue { log.entries.append("next") }
        if case .success = await failing.result { XCTFail("expected failure") }
        _ = await next.result
        XCTAssertEqual(log.entries, ["next"])
    }

    // An operation that read the generation before an invalidation sees it
    // as obsolete after its await, whatever ran in between.
    @MainActor
    func testInvalidationFencesAnOperationAcrossItsAwait() async {
        let queue = NativePushOperationQueue()
        let log = Log()
        let generation = queue.generation
        let operation = queue.enqueue {
            await withCheckedContinuation { log.release = $0 }
            log.entries.append(queue.isCurrent(generation) ? "saved" : "fenced")
        }
        while log.release == nil { await Task.yield() }
        queue.invalidate()
        log.release?.resume()
        _ = await operation.result
        XCTAssertEqual(log.entries, ["fenced"])
        XCTAssertTrue(queue.isCurrent(queue.generation))
    }

    func testConfirmationIsAcceptedOnlyForThePendingDestination() {
        XCTAssertTrue(RelayConfirmation.matches("aa", "production", token: "aa", env: "production"))
        // A late challenge sealed for the previous token or env (review I2).
        XCTAssertFalse(RelayConfirmation.matches("bb", "production", token: "aa", env: "production"))
        XCTAssertFalse(RelayConfirmation.matches("aa", "sandbox", token: "aa", env: "production"))
        // A relay that does not say which destination it confirmed.
        XCTAssertFalse(RelayConfirmation.matches(nil, nil, token: "aa", env: "production"))
        XCTAssertFalse(RelayConfirmation.matches("aa", nil, token: "aa", env: "production"))
    }
}
