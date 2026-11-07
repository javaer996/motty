import ActivityKit
import Foundation

/// Shared between the app (which starts and updates the activity) and the
/// widget extension (which renders it). Keep this file identical in both
/// targets; ActivityKit matches them by the type name.
struct SessionActivityAttributes: ActivityAttributes {
    struct ContentState: Codable, Hashable {
        var title: String
        var subtitle: String
        var connected: Int
        var total: Int
        var phase: String
        var since: Date
        var updatedAt: Date
    }

    var activityID: String
}
