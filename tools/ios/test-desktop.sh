#!/bin/sh
# Run the desktop gesture regression checks on an already booted iOS Simulator.
# Usage: tools/ios/test-desktop.sh [simulator-UDID-or-booted]
set -eu

ROOT=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
DEVELOPER_DIR=${DEVELOPER_DIR:-/Applications/Xcode.app/Contents/Developer}
export DEVELOPER_DIR
DESKTOP_TEST_DIR=$(mktemp -d "${TMPDIR:-/tmp}/motty-desktop-tests.XXXXXX")
trap 'rm -rf "$DESKTOP_TEST_DIR"' EXIT

sdk_path=$(xcrun --sdk iphonesimulator --show-sdk-path)
simulator=${1:-booted}
sim_arch=$(uname -m)
xcrun --sdk iphonesimulator swiftc \
    -sdk "$sdk_path" -target "$sim_arch-apple-ios17.0-simulator" \
    "$ROOT/ios/Motty/Desktop/DesktopUIView.swift" \
    "$ROOT/tools/ios/DesktopSurfaceTests.swift" \
    -o "$DESKTOP_TEST_DIR/desktop-tests"
xcrun simctl spawn "$simulator" "$DESKTOP_TEST_DIR/desktop-tests"
xcrun --sdk iphonesimulator swiftc \
    -sdk "$sdk_path" -target "$sim_arch-apple-ios17.0-simulator" \
    "$ROOT/ios/Motty/Models/Models.swift" \
    "$ROOT/ios/Motty/App/AppLanguage.swift" \
    "$ROOT/ios/Motty/Bridge/MottyBridge.swift" \
    "$ROOT/ios/Motty/Desktop/DesktopUIView.swift" \
    "$ROOT/tools/ios/DesktopBridgeTests.swift" \
    -o "$DESKTOP_TEST_DIR/desktop-bridge-tests"
xcrun simctl spawn "$simulator" "$DESKTOP_TEST_DIR/desktop-bridge-tests"
