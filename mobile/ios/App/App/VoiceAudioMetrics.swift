import Foundation

// Main-queue bookkeeping only. All times are injected monotonic microseconds;
// none of these counters claims to measure the DAC or an audible underrun.
final class VoiceAudioMetrics {
    struct Activation: Decodable {
        let v: Int
        let active: Bool
        let intervalMs: Int
        var enabled: Bool { v == 1 && active && intervalMs == 1000 }

        private enum CodingKeys: String, CodingKey {
            case v, active
            case intervalMs = "interval_ms"
        }
    }

    struct Microseconds: Encodable, Equatable {
        private(set) var n: Int64 = 0
        private(set) var min: Int64?
        private(set) var max: Int64?
        private(set) var sum: Int64?

        mutating func observe(_ value: Int64) {
            guard value >= 0, n < 1_000_000 else { return }
            let value = Swift.min(value, 3_600_000_000)
            min = Swift.min(min ?? value, value)
            max = Swift.max(max ?? value, value)
            sum = (sum ?? 0) + value
            n += 1
        }
    }

    struct Margin: Encodable {
        let n: Int64 = 0
        var invalidN: Int64 = 0
        let lastValid: Int64 = 0
        // scheduleBuffer(at:nil) has no demonstrated sample timeline anchor.
        let invalidReason: Int64 = 3
    }

    struct Report: Encodable {
        let type = "audio_metrics"
        let v: Int64 = 1
        let client: Int64 = 1
        var seq: Int64 = 0
        var windowStartMs: Int64 = 0
        var windowMs: Int64 = 0
        var rxFrames: Int64 = 0
        var rxBytes: Int64 = 0
        var rxOddFrames: Int64 = 0
        var rxDtUs = Microseconds()
        var scheduleUs = Microseconds()
        var scheduledFrames: Int64 = 0
        var scheduledSamples: Int64 = 0
        var rendererGeneration: Int64 = 0
        var margin = Margin()
        var mainTimerDtUs = Microseconds()
        var mainHitches: Int64 = 0
        var restarts: Int64 = 0
        var interrupts: Int64 = 0
        var pendingDropFrames: Int64 = 0
        var pendingDropBytes: Int64 = 0
        var playbackDropFrames: Int64 = 0
        var playbackDropSamples: Int64 = 0
        var reportsSkipped: Int64 = 0
        var queuedSamples: Int64 = 0
        var queuedSamplesMin: Int64 = 0
        var queuedSamplesMax: Int64 = 0
        var renderProgressSamples: Int64 = 0
        var renderObservations: Int64 = 0
        var renderInvalid: Int64 = 0
        var completionDelayUs = Microseconds()
        var trace: [[Int64]] = []
        var traceDropped: Int64 = 0
    }

    private(set) var active = true
    private(set) var generation: Int64 = 0
    private(set) var depth: Int64 = 0
    private let originUs: Int64
    private var windowStartMs: Int64 = 0
    private var lastAttemptUs: Int64
    private var lastReceiveUs: Int64?
    private var lastTimerUs: Int64
    private var renderSample: Int64?
    private var sequence: Int64 = 0
    private var traceSequence: Int64 = 0
    private var traceDropped: Int64 = 0
    private var inFlight: Int64?
    private var window = Report()

    init(nowUs: Int64, queuedSamples: Int = 0) {
        let startUs = Swift.max(0, nowUs)
        originUs = startUs
        lastAttemptUs = startUs
        lastTimerUs = startUs
        depth = Self.bounded(queuedSamples, limit: 33_554_432)
        resetWindow()
    }

    private static func bounded(_ value: Int, limit: Int64) -> Int64 {
        Int64(Swift.max(0, Swift.min(value, Int(limit))))
    }

    private static func add(_ target: inout Int64, _ value: Int64 = 1, limit: Int64 = 1_000_000) {
        target = Swift.min(limit, target + Swift.max(0, Swift.min(value, limit)))
    }

    private func resetWindow() {
        window = Report()
        window.queuedSamples = depth
        window.queuedSamplesMin = depth
        window.queuedSamplesMax = depth
    }

    private func observeDepth() {
        window.queuedSamples = depth
        window.queuedSamplesMin = Swift.min(window.queuedSamplesMin, depth)
        window.queuedSamplesMax = Swift.max(window.queuedSamplesMax, depth)
    }

    // Kinds: playing, natural drained, interrupt before/after, route rebuild
    // before/after, stop before/after. Depth is callback accounting, not DAC.
    func transition(kind: Int64, nowUs: Int64, before: Int, after: Int, frames: Int) {
        guard active, (1...8).contains(kind), nowUs >= originUs,
              nowUs - originUs <= 86_400_000_000, traceSequence < 2_147_483_647 else { return }
        traceSequence += 1
        guard window.trace.count < 24 else { traceDropped = Swift.min(2_147_483_647, traceDropped + 1); return }
        window.trace.append([traceSequence, nowUs - originUs, kind, 0, generation,
                             Self.bounded(before, limit: 33_554_432), Self.bounded(after, limit: 33_554_432),
                             Self.bounded(frames, limit: 1_000_000)])
    }

    func observeQueued(samples: Int) {
        guard active else { return }
        depth = Self.bounded(samples, limit: 33_554_432)
        observeDepth()
    }

    @discardableResult
    func beginGeneration(queuedSamples: Int = 0, discardedFrames: Int = 0, discardedSamples: Int = 0,
                         restart: Bool = false, interrupt: Bool = false) -> Int64 {
        guard active else { return generation }
        guard generation < 2_147_483_647 else { stop(); return generation }
        generation += 1
        renderSample = nil
        window.margin = Margin()
        depth = Self.bounded(queuedSamples, limit: 33_554_432)
        observeDepth()
        playbackDropped(frames: discardedFrames, samples: discardedSamples)
        if restart { Self.add(&window.restarts) }
        if interrupt { Self.add(&window.interrupts) }
        return generation
    }

    func received(byteCount: Int, nowUs: Int64) {
        guard active, byteCount >= 0, nowUs >= 0 else { return }
        Self.add(&window.rxFrames)
        Self.add(&window.rxBytes, Self.bounded(byteCount, limit: 67_108_864), limit: 67_108_864)
        if byteCount % 2 != 0 { Self.add(&window.rxOddFrames) }
        if let last = lastReceiveUs, nowUs >= last { window.rxDtUs.observe(nowUs - last) }
        if nowUs >= (lastReceiveUs ?? nowUs) { lastReceiveUs = nowUs }
    }

    func pendingDropped(byteCount: Int) {
        guard active else { return }
        Self.add(&window.pendingDropFrames)
        Self.add(&window.pendingDropBytes, Self.bounded(byteCount, limit: 67_108_864), limit: 67_108_864)
    }

    func playbackDropped(frames: Int = 1, samples: Int) {
        guard active else { return }
        Self.add(&window.playbackDropFrames, Self.bounded(frames, limit: 1_000_000))
        Self.add(&window.playbackDropSamples, Self.bounded(samples, limit: 33_554_432), limit: 33_554_432)
    }

    func scheduled(samples: Int, generation token: Int64, startedUs: Int64, endedUs: Int64) {
        guard active, token == generation, samples > 0 else { return }
        Self.add(&window.scheduledFrames)
        let samples = Self.bounded(samples, limit: 33_554_432)
        Self.add(&window.scheduledSamples, samples, limit: 33_554_432)
        depth = Swift.min(33_554_432, depth + samples)
        observeDepth()
        if startedUs >= 0, endedUs >= startedUs { window.scheduleUs.observe(endedUs - startedUs) }
    }

    func completed(samples: Int, generation token: Int64, callbackUs: Int64, mainUs: Int64) {
        guard active, token == generation, samples >= 0 else { return }
        depth = Swift.max(0, depth - Self.bounded(samples, limit: 33_554_432))
        observeDepth()
        if callbackUs >= 0, mainUs >= callbackUs { window.completionDelayUs.observe(mainUs - callbackUs) }
    }

    func observeRender(sampleTime: Int64?, sampleRate: Double?, generation token: Int64) {
        guard active, token == generation else { return }
        Self.add(&window.renderObservations)
        Self.add(&window.margin.invalidN)
        guard let sample = sampleTime, sample >= 0, let rate = sampleRate,
              rate.isFinite, rate == 24_000, sample >= (renderSample ?? sample) else {
            Self.add(&window.renderInvalid)
            renderSample = nil
            return
        }
        if let previous = renderSample {
            Self.add(&window.renderProgressSamples, Swift.min(sample - previous, 33_554_432), limit: 33_554_432)
        }
        renderSample = sample
    }

    func timerFired(nowUs: Int64) {
        guard active, nowUs >= lastTimerUs else { return }
        let delta = nowUs - lastTimerUs
        window.mainTimerDtUs.observe(delta)
        if delta > 150_000 { Self.add(&window.mainHitches) }
        lastTimerUs = nowUs
    }

    // Busy reports are skipped, not queued. The next window retains the deltas.
    // Windows longer than the wire bound are discarded as uncovered observations.
    func takeReport(nowUs: Int64, force: Bool = false) -> Report? {
        guard active, nowUs >= lastAttemptUs, nowUs - lastAttemptUs >= (force ? 1 : 1_000_000),
              nowUs >= originUs else { return nil }
        lastAttemptUs = nowUs
        let elapsedMs = (nowUs - originUs + 999) / 1_000
        guard elapsedMs <= 86_400_000, sequence < 2_147_483_647 else { stop(); return nil }
        if elapsedMs - windowStartMs > 60_000 {
            traceDropped = Swift.min(2_147_483_647, traceDropped + Int64(window.trace.count))
            windowStartMs = elapsedMs
            resetWindow()
            return nil
        }
        guard inFlight == nil else {
            Self.add(&window.reportsSkipped)
            return nil
        }
        guard elapsedMs > windowStartMs else { return nil }
        sequence += 1
        window.seq = sequence
        window.windowStartMs = windowStartMs
        window.windowMs = elapsedMs - windowStartMs
        window.rendererGeneration = generation
        window.traceDropped = traceDropped
        let result = window
        inFlight = sequence
        windowStartMs = elapsedMs
        resetWindow()
        return result
    }

    func reportFailed(traceCount: Int) {
        guard active else { return }
        traceDropped = Swift.min(2_147_483_647, traceDropped + Self.bounded(traceCount, limit: 24))
        Self.add(&window.reportsSkipped)
    }

    // Send identity is independent of renderer resets: it only releases this
    // specific outstanding send, never updates audio-generation observations.
    func reportFinished(sequence: Int64) {
        guard active, inFlight == sequence else { return }
        inFlight = nil
    }

    func stop() {
        active = false
        renderSample = nil
        inFlight = nil
    }
}
