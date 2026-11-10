import Foundation
import UIKit

// Compile the production bridge and models, replacing only the C ABI. The
// first wheel call blocks while control calls and login events must proceed.
private final class CoreStub: @unchecked Sendable {
    static let shared = CoreStub()
    let lock = NSLock()
    let wheelStarted = DispatchSemaphore(value: 0)
    let releaseWheel = DispatchSemaphore(value: 0)
    let releaseFrame = DispatchSemaphore(value: 0)
    var frameCalls = 0
    var calls: [(String, UInt32)] = []
    var events: [Data] = []
    var blockWheel = true
    var state = "connected"
    var pointerTransitions: [UInt8] = []

    func desktop(_ id: UInt32) -> [String: Any] {
        ["id": id, "hostId": "desk", "label": "desk", "address": "127.0.0.1",
         "protocol": "vnc", "state": state, "encrypted": false, "width": 400, "height": 300, "attempt": 1]
    }

    func reply(_ method: String, params: [String: Any]) -> Data {
        lock.lock()
        let id = (params["id"] as? NSNumber)?.uint32Value ?? 0
        calls.append((method, id))
        if method == "desktop.pointer", id == 3 {
            pointerTransitions.append((params["buttons"] as! NSNumber).uint8Value)
        }
        let blocked = method == "desktop.wheel" && blockWheel
        if blocked { blockWheel = false }
        var result: [String: Any] = [:]
        switch method {
        case "desktop.status": result = ["desktops": [desktop(id)]]
        case "desktop.open": result = ["desktop": desktop(2)]
        default: result = ["sent": true, "closed": true, "answered": true]
        }
        lock.unlock()
        if blocked {
            wheelStarted.signal()
            precondition(releaseWheel.wait(timeout: .now() + 8) == .success, "test never released the wheel")
        }
        return try! JSONSerialization.data(withJSONObject: ["ok": true, "result": result])
    }
}

func MottyStart(_ directory: UnsafeMutablePointer<CChar>, _ version: UnsafeMutablePointer<CChar>) -> UnsafeMutablePointer<CChar>? {
    strdup("{\"ok\":true,\"result\":{\"reused\":false}}")
}

func MottyCall(_ method: UnsafeMutablePointer<CChar>, _ params: UnsafeMutablePointer<CChar>) -> UnsafeMutablePointer<CChar>? {
    let values = (try? JSONSerialization.jsonObject(with: Data(String(cString: params).utf8))) as? [String: Any] ?? [:]
    return strdup(String(decoding: CoreStub.shared.reply(String(cString: method), params: values), as: UTF8.self))
}

func MottyFree(_ pointer: UnsafeMutablePointer<CChar>) { free(pointer) }

func MottyDesktopFrame(_ id: UInt32, _ version: UInt64, _ buffer: UnsafeMutableRawPointer?, _ capacity: Int32) -> Int32 {
    precondition(!Thread.isMainThread, "frame copying blocked UIKit's main thread")
    guard id == 4 || id == 5, version == 0 else { return 0 }
    let stub = CoreStub.shared
    if id == 4 {
        stub.lock.lock()
        stub.frameCalls += 1
        stub.lock.unlock()
        precondition(stub.releaseFrame.wait(timeout: .now() + 8) == .success, "UI could not release a stalled frame")
    }
    let needed = 40 + 40 * 30 * 4
    guard let buffer, Int(capacity) >= needed else { return -Int32(needed) }
    buffer.initializeMemory(as: UInt8.self, repeating: 0, count: needed)
    buffer.storeBytes(of: UInt64(id), as: UInt64.self)
    for (offset, value) in [(8, 40), (12, 30), (16, 1), (32, 40), (36, 30)] {
        buffer.storeBytes(of: Int32(value), toByteOffset: offset, as: Int32.self)
    }
    return Int32(needed)
}

func MottyPollEvent(_ buffer: UnsafeMutableRawPointer?, _ capacity: Int32) -> Int32 {
    let stub = CoreStub.shared
    stub.lock.lock()
    defer { stub.lock.unlock() }
    guard let data = stub.events.first, let buffer else { return 0 }
    guard data.count <= Int(capacity) else { return -Int32(data.count) }
    data.copyBytes(to: buffer.assumingMemoryBound(to: UInt8.self), count: data.count)
    stub.events.removeFirst()
    return Int32(data.count)
}

@main
private struct DesktopBridgeTests {
    static func until(_ message: String, _ condition: () -> Bool) {
        let deadline = Date().addingTimeInterval(2)
        while !condition() && Date() < deadline {
            RunLoop.main.run(until: Date().addingTimeInterval(0.01))
        }
        precondition(condition(), message)
    }

    static func main() {
        let stub = CoreStub.shared
        let bridge = MottyBridge.shared
        bridge.start()
        var refreshed = false
        bridge.refreshDesktop(1) { _ in refreshed = true }
        until("initial state did not arrive") { refreshed }
        bridge.desktopWheel(desktopID: 1, x: 10, y: 10, dx: 0, dy: 2)
        precondition(stub.wheelStarted.wait(timeout: .now() + 2) == .success, "wheel did not start")
        // Queue work from the old connection while its first write is blocked.
        for _ in 0..<200 {
            bridge.desktopPointer(desktopID: 1, x: 10, y: 20, buttons: 0)
            bridge.desktopWheel(desktopID: 1, x: 10, y: 10, dx: 0, dy: 1)
        }
        bridge.desktopKey(desktopID: 1, key: "control", down: true)
        bridge.desktopKey(desktopID: 1, key: "control", down: false)
        bridge.desktopPointer(desktopID: 3, x: 10, y: 10, buttons: 1)
        bridge.desktopPointer(desktopID: 3, x: 10, y: 10, buttons: 0)

        stub.lock.lock()
        stub.state = "password"
        stub.events.append(try! JSONSerialization.data(withJSONObject: [
            "type": "desktop.state", "desktop": stub.desktop(1)
        ]))
        stub.events.append(try! JSONSerialization.data(withJSONObject: [
            "type": "desktop.prompt", "desktopId": 1, "kind": "password", "attempt": 1
        ]))
        stub.lock.unlock()
        refreshed = false
        bridge.refreshDesktop(1) { _ in refreshed = true }
        bridge.pollEvents()
        until("blocked input hid the reconnect login prompt") {
            refreshed && bridge.desktops[1]?.state == "password" && bridge.desktopPrompts[1] != nil
        }
        bridge.desktopAnswer(desktopID: 1, kind: "password", value: "test")
        bridge.desktopRefresh(desktopID: 1)
        bridge.closeDesktop(1)
        var reopened = false
        Task {
            var host = Host()
            host.id = "desk"
            _ = try! await bridge.openDesktop(host: host, width: 400, height: 300, scale: 1)
            reopened = true
        }
        until("blocked input prevented close/reopen") { reopened && bridge.desktops[1]?.state == "closed" }
        until("the new connection did not expose its login state") { bridge.desktops[2]?.state == "password" }
        stub.lock.lock()
        var stale = stub.desktop(1)
        stale["state"] = "connected"
        stub.events.append(try! JSONSerialization.data(withJSONObject: ["type": "desktop.state", "desktop": stale]))
        stub.events.append(try! JSONSerialization.data(withJSONObject: [
            "type": "desktop.prompt", "desktopId": 1, "kind": "password", "attempt": 1
        ]))
        stub.lock.unlock()
        bridge.pollEvents()
        RunLoop.main.run(until: Date().addingTimeInterval(0.3))
        precondition(bridge.desktops[1]?.state == "closed" && bridge.desktopPrompts[1] == nil,
                     "late events revived the closed connection")
        stub.releaseWheel.signal()
        // A text input on an independent id is a barrier behind the blocked
        // call and any old work: stale keys and wheel events must be discarded.
        bridge.desktopText(desktopID: 3, text: "barrier")
        until("input queue did not resume") {
            stub.lock.lock()
            defer { stub.lock.unlock() }
            return stub.calls.contains { $0 == ("desktop.text", 3) }
        }
        stub.lock.lock()
        let inputs = stub.calls.filter { $0.1 == 1 && ["desktop.key", "desktop.pointer", "desktop.wheel"].contains($0.0) }
        let answered = stub.calls.contains { $0 == ("desktop.answer", 1) }
        let refresh = stub.calls.contains { $0 == ("desktop.refresh", 1) }
        let transitions = stub.pointerTransitions
        stub.lock.unlock()
        precondition(inputs.count == 1, "queued input from the dead connection was replayed")
        precondition(answered && refresh, "login answer or manual refresh was blocked by input")
        precondition(transitions == [1, 0], "button press/release was dropped as expired motion")
        print("PASS: blocked input cannot hide login/refresh/reopen; stale input and late events discarded; button press/release preserved")

        // Exercise the real surface and bridge together. Input separation did
        // not previously protect the main thread from a blocked frame copy.
        let surface = DesktopSurfaceView(frame: CGRect(x: 0, y: 0, width: 400, height: 300))
        surface.desktopID = 4
        let window = UIWindow(frame: surface.frame)
        window.addSubview(surface)
        until("frame worker never started") {
            stub.lock.lock()
            defer { stub.lock.unlock() }
            return stub.frameCalls == 1
        }
        stub.lock.lock()
        stub.events.append(try! JSONSerialization.data(withJSONObject: ["type": "desktop.state", "desktop": stub.desktop(4)]))
        stub.events.append(try! JSONSerialization.data(withJSONObject: [
            "type": "desktop.prompt", "desktopId": 4, "kind": "password", "attempt": 1
        ]))
        stub.lock.unlock()
        bridge.pollEvents()
        until("stalled frame prevented the login UI state from appearing") { bridge.desktopPrompts[4] != nil }
        RunLoop.main.run(until: Date().addingTimeInterval(0.2))
        stub.lock.lock()
        let frameCalls = stub.frameCalls
        stub.lock.unlock()
        precondition(frameCalls == 1, "display ticks queued multiple blocked frame reads")
        bridge.closeDesktop(4)
        until("stalled frame prevented disconnect") { bridge.desktops[4]?.state == "closed" }
        surface.desktopID = 5
        until("new desktop waited for the old blocked frame") {
            Mirror(reflecting: surface).children.first { $0.label == "frameVersion" }?.value as? UInt64 == 5
        }
        stub.releaseFrame.signal()
        RunLoop.main.run(until: Date().addingTimeInterval(0.1))
        precondition(Mirror(reflecting: surface).children.first { $0.label == "frameVersion" }?.value as? UInt64 == 5,
                     "late old frame overwrote the reconnected desktop")
        surface.stop()
        print("PASS: stalled frame keeps login/disconnect responsive; one frame in flight; old frame cannot overwrite reconnected desktop")
    }
}
