import AVFoundation
import Combine
import UIKit

/// Keeps the process running while the phone is locked by looping a silent
/// audio file with the `audio` background mode.
///
/// A Live Activity alone does not grant background execution, so without this
/// iOS suspends the app a few seconds after the screen locks and the SSH
/// keepalive stops. This is the same mechanism music apps use to keep playing;
/// for an SSH client it is a deliberate trade-off between a live connection
/// and battery. The setting is exposed in Settings and defaults to on.
final class BackgroundSessionKeeper {
    static let shared = BackgroundSessionKeeper()

    private var player: AVAudioPlayer?
    private var sessions: [SessionInfo] = []
    private var isBackground = false
    private var needsAudio = false
    private var backgroundTask: UIBackgroundTaskIdentifier = .invalid
    private var cancellables = Set<AnyCancellable>()
    private var started = false

    private var enabled: Bool {
        UserDefaults.standard.object(forKey: "keepAliveInBackground") as? Bool ?? true
    }

    private init() {}

    func start() {
        guard !started else { return }
        started = true

        MottyBridge.shared.$sessions
            .receive(on: DispatchQueue.main)
            .sink { [weak self] sessions in
                self?.sessions = sessions
                self?.apply()
            }
            .store(in: &cancellables)

        NotificationCenter.default.publisher(for: UserDefaults.didChangeNotification)
            .receive(on: DispatchQueue.main)
            .sink { [weak self] _ in self?.apply() }
            .store(in: &cancellables)

        NotificationCenter.default.addObserver(
            forName: AVAudioSession.interruptionNotification,
            object: AVAudioSession.sharedInstance(),
            queue: .main
        ) { [weak self] note in
            self?.handleInterruption(note)
        }
    }

    /// Called from the scene phase. `.inactive` (Control Center, a call) does
    /// not need the audio yet; only `.background` does.
    func setBackground(_ background: Bool) {
        isBackground = background
        apply()
    }

    private func apply() {
        let hasOpenSession = sessions.contains { $0.isConnected || $0.isWaiting }
        let shouldRun = enabled && isBackground && hasOpenSession
        needsAudio = shouldRun
        if shouldRun {
            startAudio()
        } else {
            stopAudio()
        }
    }

    private func startAudio() {
        guard player == nil else { return }
        do {
            let session = AVAudioSession.sharedInstance()
            try session.setCategory(.playback, mode: .default, options: [.mixWithOthers])
            try session.setActive(true)
            let player = try AVAudioPlayer(data: Self.silentWAV())
            player.numberOfLoops = -1
            player.volume = 1
            player.prepareToPlay()
            guard player.play() else {
                try? session.setActive(false, options: [.notifyOthersOnDeactivation])
                return
            }
            self.player = player
            beginTask()
        } catch {
            #if DEBUG
            print("Background keepalive audio failed: \(error)")
            #endif
        }
    }

    private func stopAudio() {
        guard player != nil else { return }
        player?.stop()
        player = nil
        try? AVAudioSession.sharedInstance().setActive(false, options: [.notifyOthersOnDeactivation])
        endTask()
    }

    /// Audio playback keeps the app alive on its own; this task only covers
    /// the transition until the first silent buffer is playing.
    private func beginTask() {
        guard backgroundTask == .invalid else { return }
        backgroundTask = UIApplication.shared.beginBackgroundTask(withName: "Motty SSH keepalive") { [weak self] in
            self?.endTask()
        }
        DispatchQueue.main.asyncAfter(deadline: .now() + 2) { [weak self] in
            self?.endTask()
        }
    }

    private func endTask() {
        guard backgroundTask != .invalid else { return }
        UIApplication.shared.endBackgroundTask(backgroundTask)
        backgroundTask = .invalid
    }

    private func handleInterruption(_ note: Notification) {
        guard let raw = note.userInfo?[AVAudioSessionInterruptionTypeKey] as? UInt,
              let type = AVAudioSession.InterruptionType(rawValue: raw) else { return }
        switch type {
        case .began:
            // The system paused our player; drop it so the ended handler can
            // create a fresh one.
            player = nil
        case .ended:
            if needsAudio {
                startAudio()
            }
        @unknown default:
            break
        }
    }

    /// One second of 8 kHz mono 16-bit PCM silence. AVAudioPlayer loops it
    /// forever; keeping the samples silent is what makes the keepalive
    /// inaudible.
    private static func silentWAV(seconds: Double = 1, sampleRate: Int = 8000) -> Data {
        let sampleCount = Int(Double(sampleRate) * seconds)
        let dataSize = sampleCount * 2
        var data = Data()
        data.append(contentsOf: Array("RIFF".utf8))
        appendLE(UInt32(36 + dataSize), to: &data)
        data.append(contentsOf: Array("WAVEfmt ".utf8))
        appendLE(UInt32(16), to: &data)
        appendLE(UInt16(1), to: &data)                 // PCM
        appendLE(UInt16(1), to: &data)                 // mono
        appendLE(UInt32(sampleRate), to: &data)
        appendLE(UInt32(sampleRate * 2), to: &data)    // byte rate
        appendLE(UInt16(2), to: &data)                 // block align
        appendLE(UInt16(16), to: &data)                // bits per sample
        data.append(contentsOf: Array("data".utf8))
        appendLE(UInt32(dataSize), to: &data)
        data.append(Data(repeating: 0, count: dataSize))
        return data
    }

    private static func appendLE<T: FixedWidthInteger>(_ value: T, to data: inout Data) {
        withUnsafeBytes(of: value.littleEndian) { data.append(contentsOf: $0) }
    }
}
