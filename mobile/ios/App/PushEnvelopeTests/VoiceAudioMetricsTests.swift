import Foundation
import XCTest

final class VoiceAudioMetricsTests: XCTestCase {
    func testOnlyCompatibleTypedActivationEnables() throws {
        let decoder = JSONDecoder()
        let good = Data(#"{"v":1,"active":true,"interval_ms":1000}"#.utf8)
        XCTAssertTrue(try decoder.decode(VoiceAudioMetrics.Activation.self, from: good).enabled)
        for text in [
            #"{"v":2,"active":true,"interval_ms":1000}"#,
            #"{"v":1,"active":false,"interval_ms":1000}"#,
            #"{"v":1,"active":true,"interval_ms":500}"#,
            #"{"v":true,"active":true,"interval_ms":1000}"#,
            #"{"v":1,"active":1,"interval_ms":1000}"#,
            #"{"v":1,"active":true,"interval_ms":"1000"}"#,
            #"{}"#
        ] {
            XCTAssertNotEqual((try? decoder.decode(VoiceAudioMetrics.Activation.self, from: Data(text.utf8)))?.enabled, true)
        }
    }

    private func report(_ metrics: VoiceAudioMetrics, at time: Int64 = 1_000_000) throws -> VoiceAudioMetrics.Report {
        try XCTUnwrap(metrics.takeReport(nowUs: time))
    }

    private func schedule(_ metrics: VoiceAudioMetrics, samples: Int = 480, token: Int64? = nil) {
        metrics.scheduled(samples: samples, generation: token ?? metrics.generation, startedUs: 40, endedUs: 75)
    }

    func testThreeReceivesAndPendingReplayDoNotCountTwice() throws {
        let metrics = VoiceAudioMetrics(nowUs: 0)
        for time in [Int64(0), 20_000, 40_000] { metrics.received(byteCount: 960, nowUs: time) }
        let token = metrics.beginGeneration()
        for _ in 0..<3 { schedule(metrics, token: token) }
        let result = try report(metrics)
        XCTAssertEqual(result.rxFrames, 3)
        XCTAssertEqual(result.rxBytes, 2880)
        XCTAssertEqual(result.rxOddFrames, 0)
        XCTAssertEqual(result.rxDtUs.n, 2)
        XCTAssertEqual(result.rxDtUs.min, 20_000)
        XCTAssertEqual(result.rxDtUs.max, 20_000)
        XCTAssertEqual(result.rxDtUs.sum, 40_000)
        XCTAssertEqual(result.scheduledFrames, 3)
        XCTAssertEqual(result.scheduledSamples, 1440)
        XCTAssertEqual(result.queuedSamples, 1440)
        XCTAssertEqual(result.queuedSamplesMin, 0)
        XCTAssertEqual(result.queuedSamplesMax, 1440)
        XCTAssertEqual(result.scheduleUs.n, 3)
        XCTAssertEqual(result.scheduleUs.sum, 105)
    }

    func testWindowsAreDeltasAndReceiveGapSurvivesSnapshot() throws {
        let metrics = VoiceAudioMetrics(nowUs: 50_000)
        metrics.received(byteCount: 960, nowUs: 60_000)
        let first = try report(metrics, at: 1_050_000)
        metrics.reportFinished(sequence: first.seq)
        metrics.received(byteCount: 961, nowUs: 1_060_000)
        let second = try report(metrics, at: 2_175_000)
        XCTAssertEqual(first.windowStartMs, 0)
        XCTAssertEqual(first.windowMs, 1000)
        XCTAssertEqual(second.seq, 2)
        XCTAssertEqual(second.windowStartMs, 1000)
        XCTAssertEqual(second.windowMs, 1125)
        XCTAssertEqual(second.rxFrames, 1)
        XCTAssertEqual(second.rxBytes, 961)
        XCTAssertEqual(second.rxOddFrames, 1)
        XCTAssertEqual(second.rxDtUs.n, 1)
        XCTAssertEqual(second.rxDtUs.sum, 1_000_000)
        metrics.reportFinished(sequence: second.seq)
        let empty = try report(metrics, at: 3_175_000)
        XCTAssertEqual(empty.rxFrames, 0)
        XCTAssertEqual(empty.rxDtUs.n, 0)
        XCTAssertNil(empty.rxDtUs.min)
    }

    func testCompletionDelayAndGenerationFence() throws {
        let metrics = VoiceAudioMetrics(nowUs: 0)
        let old = metrics.beginGeneration()
        schedule(metrics)
        metrics.beginGeneration(discardedFrames: 1, discardedSamples: 480, restart: true)
        schedule(metrics, samples: 960)
        metrics.completed(samples: 480, generation: old, callbackUs: 100_000, mainUs: 180_000)
        XCTAssertEqual(metrics.depth, 960)
        metrics.completed(samples: 480, generation: metrics.generation, callbackUs: 100_000, mainUs: 180_000)
        let result = try report(metrics)
        XCTAssertEqual(result.queuedSamples, 480)
        XCTAssertEqual(result.completionDelayUs.n, 1)
        XCTAssertEqual(result.completionDelayUs.sum, 80_000)
        XCTAssertEqual(result.playbackDropFrames, 1)
        XCTAssertEqual(result.playbackDropSamples, 480)
        XCTAssertEqual(result.restarts, 1)
        XCTAssertEqual(result.rendererGeneration, 2)
    }

    func testNilRegressionAndRatesCannotFabricateRendererProgressOrMargin() throws {
        let metrics = VoiceAudioMetrics(nowUs: 0)
        let token = metrics.beginGeneration()
        metrics.observeRender(sampleTime: nil, sampleRate: nil, generation: token)
        metrics.observeRender(sampleTime: -1, sampleRate: 24_000, generation: token)
        metrics.observeRender(sampleTime: 0, sampleRate: .nan, generation: token)
        metrics.observeRender(sampleTime: 0, sampleRate: 48_000, generation: token)
        metrics.observeRender(sampleTime: 0, sampleRate: 0, generation: token)
        metrics.observeRender(sampleTime: 0, sampleRate: 24_000, generation: token)
        metrics.observeRender(sampleTime: 120, sampleRate: 24_000, generation: token)
        metrics.observeRender(sampleTime: 100, sampleRate: 24_000, generation: token)
        metrics.observeRender(sampleTime: 300, sampleRate: 24_000, generation: token)
        metrics.observeRender(sampleTime: 360, sampleRate: 24_000, generation: token)
        let result = try report(metrics)
        XCTAssertEqual(result.renderObservations, 10)
        XCTAssertEqual(result.renderInvalid, 6)
        XCTAssertEqual(result.renderProgressSamples, 180)
        XCTAssertEqual(result.margin.n, 0)
        XCTAssertEqual(result.margin.invalidReason, 3)
        XCTAssertEqual(result.margin.invalidN, 10)
        XCTAssertEqual(result.margin.lastValid, 0)
    }

    func testGenerationResetsOnlyRendererBaselineNotWindowCounts() throws {
        let metrics = VoiceAudioMetrics(nowUs: 0)
        let old = metrics.beginGeneration()
        metrics.received(byteCount: 960, nowUs: 0)
        metrics.observeRender(sampleTime: 100, sampleRate: 24_000, generation: old)
        metrics.observeRender(sampleTime: 300, sampleRate: 24_000, generation: old)
        let new = metrics.beginGeneration(interrupt: true)
        metrics.observeRender(sampleTime: 10_000, sampleRate: 24_000, generation: old)
        metrics.observeRender(sampleTime: 0, sampleRate: 24_000, generation: new)
        metrics.observeRender(sampleTime: 60, sampleRate: 24_000, generation: new)
        let result = try report(metrics)
        XCTAssertEqual(result.rxFrames, 1)
        XCTAssertEqual(result.interrupts, 1)
        XCTAssertEqual(result.renderObservations, 4)
        XCTAssertEqual(result.renderProgressSamples, 260)
        XCTAssertEqual(result.margin.n, 0)
    }

    func testLateTimerIsAnObservedIntervalNotAnExactMainBlock() throws {
        let metrics = VoiceAudioMetrics(nowUs: 0)
        metrics.timerFired(nowUs: 100_000)
        metrics.timerFired(nowUs: 200_000)
        metrics.timerFired(nowUs: 2_200_000)
        metrics.timerFired(nowUs: 2_100_000)
        let result = try report(metrics, at: 2_200_000)
        XCTAssertEqual(result.mainTimerDtUs.n, 3)
        XCTAssertEqual(result.mainTimerDtUs.min, 100_000)
        XCTAssertEqual(result.mainTimerDtUs.max, 2_000_000)
        XCTAssertEqual(result.mainTimerDtUs.sum, 2_200_000)
        XCTAssertEqual(result.mainHitches, 1)
        XCTAssertEqual(result.windowMs, 2200)
    }

    func testPendingAndPlaybackDropsAreExactIncludingEmptyAndOddFrames() throws {
        let metrics = VoiceAudioMetrics(nowUs: 0)
        metrics.received(byteCount: 0, nowUs: 0)
        metrics.received(byteCount: 961, nowUs: 20_000)
        metrics.pendingDropped(byteCount: 960)
        metrics.pendingDropped(byteCount: 123)
        metrics.playbackDropped(samples: 0)
        metrics.playbackDropped(samples: 480)
        metrics.beginGeneration(discardedFrames: 2, discardedSamples: 1000, interrupt: true)
        let result = try report(metrics)
        XCTAssertEqual(result.rxFrames, 2)
        XCTAssertEqual(result.rxOddFrames, 1)
        XCTAssertEqual(result.pendingDropFrames, 2)
        XCTAssertEqual(result.pendingDropBytes, 1083)
        XCTAssertEqual(result.playbackDropFrames, 4)
        XCTAssertEqual(result.playbackDropSamples, 1480)
        XCTAssertEqual(result.interrupts, 1)
    }

    func testLateEnableHasKnownAccountingButNoTimeline() throws {
        let metrics = VoiceAudioMetrics(nowUs: 500_000, queuedSamples: 960)
        metrics.beginGeneration(queuedSamples: 960)
        metrics.observeQueued(samples: 480)
        metrics.observeRender(sampleTime: 8000, sampleRate: 24_000, generation: metrics.generation)
        let result = try report(metrics, at: 1_500_000)
        XCTAssertEqual(result.rxFrames, 0)
        XCTAssertEqual(result.scheduledFrames, 0)
        XCTAssertEqual(result.queuedSamples, 480)
        XCTAssertEqual(result.queuedSamplesMin, 480)
        XCTAssertEqual(result.queuedSamplesMax, 960)
        XCTAssertEqual(result.renderProgressSamples, 0)
        XCTAssertEqual(result.margin.invalidReason, 3)
    }

    func testBusySkipNoRetryAndStaleSendCannotReleaseNewReport() throws {
        let metrics = VoiceAudioMetrics(nowUs: 0)
        let first = try report(metrics)
        metrics.received(byteCount: 960, nowUs: 1_200_000)
        XCTAssertNil(metrics.takeReport(nowUs: 2_000_000))
        XCTAssertNil(metrics.takeReport(nowUs: 2_100_000))
        metrics.reportFinished(sequence: first.seq + 1)
        XCTAssertNil(metrics.takeReport(nowUs: 3_000_000))
        metrics.reportFinished(sequence: first.seq)
        XCTAssertNil(metrics.takeReport(nowUs: 3_100_000))
        let next = try report(metrics, at: 4_000_000)
        XCTAssertEqual(next.reportsSkipped, 2)
        XCTAssertEqual(next.rxFrames, 1)
        XCTAssertEqual(next.windowStartMs, 1000)
        XCTAssertEqual(next.windowMs, 3000)
        metrics.reportFinished(sequence: first.seq)
        XCTAssertNil(metrics.takeReport(nowUs: 5_000_000))
    }

    func testStoppedHelperIgnoresLateCallbacksAndDoesNotAffectNextCall() throws {
        let old = VoiceAudioMetrics(nowUs: 0)
        let token = old.beginGeneration()
        schedule(old)
        let sent = try report(old)
        old.stop()
        old.stop()
        old.completed(samples: 480, generation: token, callbackUs: 1, mainUs: 100)
        old.reportFinished(sequence: sent.seq)
        old.received(byteCount: 960, nowUs: 2_000_000)
        old.timerFired(nowUs: 2_000_000)
        XCTAssertNil(old.takeReport(nowUs: 2_000_000))
        XCTAssertEqual(old.depth, 480)
        let next = VoiceAudioMetrics(nowUs: 0)
        XCTAssertEqual(try report(next).rxFrames, 0)
    }

    func testLongSuspensionLeavesUncoveredWindowRatherThanOutOfRangeReport() throws {
        let metrics = VoiceAudioMetrics(nowUs: 0)
        metrics.received(byteCount: 960, nowUs: 0)
        metrics.timerFired(nowUs: 61_000_000)
        XCTAssertNil(metrics.takeReport(nowUs: 61_000_000))
        let next = try report(metrics, at: 62_000_000)
        XCTAssertEqual(next.windowStartMs, 61_000)
        XCTAssertEqual(next.windowMs, 1000)
        XCTAssertEqual(next.rxFrames, 0)
        XCTAssertEqual(next.mainTimerDtUs.n, 0)
    }

    func testRegressiveTimesAndExtremeValuesRemainBounded() throws {
        let metrics = VoiceAudioMetrics(nowUs: 0)
        metrics.received(byteCount: Int.max, nowUs: 100)
        metrics.received(byteCount: 0, nowUs: 50)
        metrics.received(byteCount: 0, nowUs: 200)
        metrics.playbackDropped(frames: Int.max, samples: Int.max)
        metrics.scheduled(samples: Int.max, generation: 0, startedUs: 10, endedUs: 5)
        metrics.completed(samples: 0, generation: 0, callbackUs: 10, mainUs: 5)
        XCTAssertNil(metrics.takeReport(nowUs: -1))
        let result = try report(metrics)
        XCTAssertEqual(result.rxBytes, 67_108_864)
        XCTAssertEqual(result.rxDtUs.n, 1)
        XCTAssertEqual(result.rxDtUs.sum, 100)
        XCTAssertEqual(result.playbackDropFrames, 1_000_000)
        XCTAssertEqual(result.playbackDropSamples, 33_554_432)
        XCTAssertEqual(result.queuedSamples, 33_554_432)
        XCTAssertEqual(result.scheduleUs.n, 0)
        XCTAssertEqual(result.completionDelayUs.n, 0)
    }

    func testTraceSeparatesNaturalDrainFromInterruptAndRebuildGenerations() throws {
        let metrics = VoiceAudioMetrics(nowUs: 100)
        metrics.beginGeneration()
        metrics.transition(kind: 1, nowUs: 200, before: 0, after: 480, frames: 1)
        schedule(metrics)
        metrics.completed(samples: 480, generation: 1, callbackUs: 300, mainUs: 400)
        metrics.transition(kind: 2, nowUs: 400, before: 480, after: 0, frames: 0)
        metrics.transition(kind: 3, nowUs: 500, before: 960, after: 960, frames: 2)
        metrics.beginGeneration(discardedFrames: 2, discardedSamples: 960, interrupt: true)
        metrics.transition(kind: 4, nowUs: 600, before: 0, after: 0, frames: 0)
        metrics.transition(kind: 5, nowUs: 700, before: 240, after: 240, frames: 1)
        metrics.beginGeneration(discardedFrames: 1, discardedSamples: 240, restart: true)
        metrics.transition(kind: 6, nowUs: 800, before: 0, after: 0, frames: 0)
        let result = try report(metrics, at: 1_000_100)
        XCTAssertEqual(result.trace.map { $0[2] }, [1, 2, 3, 4, 5, 6])
        XCTAssertEqual(result.trace.map { $0[4] }, [1, 1, 1, 2, 2, 3])
        XCTAssertEqual(result.trace[1], [2, 300, 2, 0, 1, 480, 0, 0])
        XCTAssertEqual(result.margin.n, 0)
        XCTAssertEqual(result.margin.invalidReason, 3)
    }

    func testTraceBoundAndStopKeepsActualAccountedDepth() throws {
        let metrics = VoiceAudioMetrics(nowUs: 0)
        for i in 1...30 { metrics.transition(kind: 1, nowUs: Int64(i), before: 0, after: 480, frames: 1) }
        let first = try report(metrics)
        XCTAssertEqual(first.trace.count, 24)
        XCTAssertEqual(first.traceDropped, 6)
        metrics.reportFinished(sequence: first.seq)
        metrics.transition(kind: 7, nowUs: 1_100_000, before: 480, after: 480, frames: 1)
        metrics.beginGeneration(discardedFrames: 1, discardedSamples: 480)
        metrics.transition(kind: 8, nowUs: 1_100_001, before: 480, after: 480, frames: 1)
        let final = try XCTUnwrap(metrics.takeReport(nowUs: 1_100_001, force: true))
        XCTAssertEqual(final.trace.map { $0[0] }, [31, 32])
        XCTAssertEqual(final.trace.map { $0[5] }, [480, 480])
        XCTAssertEqual(final.windowMs, 101)
        XCTAssertEqual(final.traceDropped, 6)
        let encoder = JSONEncoder()
        encoder.keyEncodingStrategy = .convertToSnakeCase
        XCTAssertLessThanOrEqual(try encoder.encode(first).count, 4096)
        metrics.stop()
        metrics.transition(kind: 1, nowUs: 2_000_000, before: 0, after: 480, frames: 1)
        XCTAssertNil(metrics.takeReport(nowUs: 2_000_000))
    }

    func testEncodedSchemaHasOnlyAllowlistedFieldsAndUnavailableMargin() throws {
        let metrics = VoiceAudioMetrics(nowUs: 0)
        let encoder = JSONEncoder()
        encoder.keyEncodingStrategy = .convertToSnakeCase
        let data = try encoder.encode(report(metrics))
        XCTAssertLessThanOrEqual(data.count, 4096)
        let json = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        let allowed = Set("type v client seq window_start_ms window_ms rx_frames rx_bytes rx_odd_frames rx_dt_us schedule_us scheduled_frames scheduled_samples renderer_generation margin main_timer_dt_us main_hitches restarts interrupts pending_drop_frames pending_drop_bytes playback_drop_frames playback_drop_samples reports_skipped queued_samples queued_samples_min queued_samples_max render_progress_samples render_observations render_invalid completion_delay_us trace trace_dropped".split(separator: " ").map(String.init))
        XCTAssertEqual(Set(json.keys), allowed)
        let margin = try XCTUnwrap(json["margin"] as? [String: Any])
        XCTAssertEqual(Set(margin.keys), Set(["n", "invalid_n", "last_valid", "invalid_reason"]))
        XCTAssertEqual(margin["invalid_reason"] as? Int, 3)
        let empty = try XCTUnwrap(json["rx_dt_us"] as? [String: Any])
        XCTAssertEqual(Set(empty.keys), Set(["n"]))
        XCTAssertEqual(json["type"] as? String, "audio_metrics")
    }
}
