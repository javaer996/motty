import ActivityKit
import Combine
import Foundation

/// Mirrors the open SSH sessions into a Live Activity so the Lock Screen and
/// the Dynamic Island keep showing what Motty is connected to.
///
/// The activity is display-only. iOS does not let a Live Activity keep the
/// app running; `BackgroundSessionKeeper` is what keeps the SSH socket alive
/// while the screen is locked.
final class SessionLiveActivityManager {
    static let shared = SessionLiveActivityManager()

    private var activity: Activity<SessionActivityAttributes>?
    private var lastState: SessionActivityAttributes.ContentState?
    private var cancellables = Set<AnyCancellable>()
    private var started = false

    private var enabled: Bool {
        UserDefaults.standard.object(forKey: "liveActivityEnabled") as? Bool ?? true
    }

    private init() {}

    func start() {
        guard !started else { return }
        started = true

        MottyBridge.shared.$sessions
            .receive(on: DispatchQueue.main)
            .sink { [weak self] sessions in self?.update(sessions) }
            .store(in: &cancellables)

        NotificationCenter.default.publisher(for: UserDefaults.didChangeNotification)
            .receive(on: DispatchQueue.main)
            .sink { [weak self] _ in self?.update(MottyBridge.shared.sessions) }
            .store(in: &cancellables)

        // A previous process may have left an activity behind. The new
        // process has no session list, so it cannot adopt it.
        Task {
            for stale in Activity<SessionActivityAttributes>.activities {
                await stale.end(nil, dismissalPolicy: .immediate)
            }
        }
    }

    private func update(_ sessions: [SessionInfo]) {
        guard enabled, ActivityAuthorizationInfo().areActivitiesEnabled else {
            end()
            return
        }

        let open = sessions.filter { $0.phase != "failed" && $0.phase != "closed" }
        let connected = open.filter(\.isConnected)
        // Start while the session is still connecting too: locking the phone
        // during the handshake should still leave something in the island.
        guard !open.isEmpty else {
            end()
            return
        }

        let primary = connected.first ?? open[0]
        let since = connected
            .compactMap(\.connectedAt)
            .min()
            .map { Date(timeIntervalSince1970: TimeInterval($0)) } ?? Date()
        let state = SessionActivityAttributes.ContentState(
            title: primary.title.isEmpty ? primary.address : primary.title,
            subtitle: "\(primary.user)@\(primary.address)",
            connected: connected.count,
            total: open.count,
            phase: primary.phase,
            since: since,
            updatedAt: Date()
        )

        if activity == nil {
            do {
                activity = try Activity.request(
                    attributes: SessionActivityAttributes(activityID: UUID().uuidString),
                    // The elapsed-time label is rendered by WidgetKit, so the
                    // content never needs to go stale while it is on screen.
                    content: ActivityContent(state: state, staleDate: nil),
                    pushType: nil
                )
                lastState = state
            } catch {
                #if DEBUG
                print("Live Activity request failed: \(error)")
                #endif
            }
        } else if state != lastState {
            lastState = state
            let activity = self.activity
            Task {
                await activity?.update(ActivityContent(state: state, staleDate: nil))
            }
        }
    }

    private func end() {
        guard let activity else { return }
        self.activity = nil
        lastState = nil
        Task {
            await activity.end(nil, dismissalPolicy: .immediate)
        }
    }
}
