import ActivityKit
import SwiftUI
import WidgetKit

private enum LAText {
    static var isChinese: Bool {
        Locale.preferredLanguages.first?.hasPrefix("zh") == true
    }

    static func connected(_ count: Int) -> String {
        isChinese ? "\(count) 个会话在线" : "\(count) online"
    }

    static func connecting() -> String {
        isChinese ? "正在连接" : "Connecting"
    }

    static func sessions(_ count: Int) -> String {
        isChinese ? "\(count) 个会话" : "\(count) sessions"
    }
}

struct SessionLiveActivityWidget: Widget {
    var body: some WidgetConfiguration {
        ActivityConfiguration(for: SessionActivityAttributes.self) { context in
            LockScreenView(context: context)
                .activityBackgroundTint(Color.black.opacity(0.92))
                .activitySystemActionForegroundColor(.green)
        } dynamicIsland: { context in
            DynamicIsland {
                DynamicIslandExpandedRegion(.leading) {
                    Image(systemName: "terminal.fill")
                        .font(.title3)
                        .foregroundStyle(.green)
                        .padding(.leading, 4)
                }
                DynamicIslandExpandedRegion(.trailing) {
                    Text(statusText(context.state))
                        .font(.caption2.weight(.semibold))
                        .foregroundStyle(context.state.connected > 0 ? .green : .orange)
                        .padding(.trailing, 4)
                }
                DynamicIslandExpandedRegion(.center) {
                    VStack(alignment: .leading, spacing: 2) {
                        Text(context.state.title)
                            .font(.subheadline.weight(.semibold))
                            .lineLimit(1)
                        Text(context.state.subtitle)
                            .font(.caption2)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                    }
                }
                DynamicIslandExpandedRegion(.bottom) {
                    HStack(spacing: 10) {
                        Label(LAText.sessions(context.state.total), systemImage: "network")
                        Spacer()
                        Text(context.state.since, style: .timer)
                            .monospacedDigit()
                    }
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                    .padding(.horizontal, 4)
                }
            } compactLeading: {
                Image(systemName: "terminal.fill")
                    .foregroundStyle(.green)
            } compactTrailing: {
                Text("\(context.state.connected)")
                    .font(.caption2.weight(.bold))
                    .monospacedDigit()
                    .foregroundStyle(context.state.connected > 0 ? .green : .orange)
            } minimal: {
                Image(systemName: "terminal.fill")
                    .foregroundStyle(context.state.connected > 0 ? .green : .orange)
            }
            .keylineTint(.green)
        }
    }

    private func statusText(_ state: SessionActivityAttributes.ContentState) -> String {
        state.connected > 0 ? LAText.connected(state.connected) : LAText.connecting()
    }
}

private struct LockScreenView: View {
    let context: ActivityViewContext<SessionActivityAttributes>

    var body: some View {
        HStack(spacing: 12) {
            ZStack {
                RoundedRectangle(cornerRadius: 10, style: .continuous)
                    .fill(Color.green.opacity(0.16))
                Image(systemName: "terminal.fill")
                    .font(.system(size: 18, weight: .semibold))
                    .foregroundStyle(.green)
            }
            .frame(width: 40, height: 40)

            VStack(alignment: .leading, spacing: 3) {
                HStack(spacing: 6) {
                    Text(context.state.title)
                        .font(.headline)
                        .lineLimit(1)
                    Circle()
                        .fill(context.state.connected > 0 ? Color.green : Color.orange)
                        .frame(width: 6, height: 6)
                }
                Text(context.state.subtitle)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                HStack(spacing: 10) {
                    Text(context.state.connected > 0
                         ? LAText.connected(context.state.connected)
                         : LAText.connecting())
                    if context.state.total > 1 {
                        Text(LAText.sessions(context.state.total))
                    }
                }
                .font(.caption2)
                .foregroundStyle(.tertiary)
            }

            Spacer(minLength: 8)

            Text(context.state.since, style: .timer)
                .font(.caption.monospacedDigit())
                .foregroundStyle(.secondary)
        }
        .padding(14)
    }
}
