#!/bin/sh
# Builds the Go core as a static library for iOS and packages it as an
# xcframework the Xcode project links against.
#
#   ios/scripts/build-go.sh [output-dir]
#
# Requires Xcode with the iOS SDK. Xcode 16 ships with the project; when
# xcode-select points at the Command Line Tools, DEVELOPER_DIR is set here so
# the build still uses the full IDE.
set -eu

ROOT=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
OUT=${1:-"$ROOT/ios/Frameworks"}
BUILD="$ROOT/ios/.build"
DEVELOPER_DIR=${DEVELOPER_DIR:-/Applications/Xcode.app/Contents/Developer}
export DEVELOPER_DIR
MIN_IOS=17.0
NAME=MottyCore

build() { # <sdk> <goarch> <min-flag> <dir>
	sdk=$1
	goarch=$2
	minflag=$3
	dir=$4
	sdk_path=$(xcrun --sdk "$sdk" --show-sdk-path)
	cc=$(xcrun --sdk "$sdk" -f clang)
	# clang spells the Go amd64 architecture x86_64.
	clang_arch=$goarch
	if [ "$goarch" = amd64 ]; then
		clang_arch=x86_64
	fi
	mkdir -p "$dir/include"
	echo "building $sdk/$goarch"
	CGO_ENABLED=1 GOOS=ios GOARCH="$goarch" CC="$cc" \
		CGO_CFLAGS="-isysroot $sdk_path $minflag -arch $clang_arch" \
		CGO_LDFLAGS="-isysroot $sdk_path $minflag -arch $clang_arch" \
		go build -buildmode=c-archive -o "$dir/libmotty.a" "$ROOT/mobile"
	mv "$dir/libmotty.h" "$dir/include/libmotty.h"
}

rm -rf "$BUILD"
build iphoneos arm64 "-miphoneos-version-min=$MIN_IOS" "$BUILD/device"
# One simulator library for both Mac architectures, so the same project builds
# on Intel and Apple Silicon.
build iphonesimulator arm64 "-mios-simulator-version-min=$MIN_IOS" "$BUILD/sim-arm64"
build iphonesimulator amd64 "-mios-simulator-version-min=$MIN_IOS" "$BUILD/sim-x86_64"
mkdir -p "$BUILD/simulator/include"
lipo -create "$BUILD/sim-arm64/libmotty.a" "$BUILD/sim-x86_64/libmotty.a" \
	-output "$BUILD/simulator/libmotty.a"
cp "$BUILD/sim-arm64/include/libmotty.h" "$BUILD/simulator/include/libmotty.h"

rm -rf "$OUT/$NAME.xcframework"
mkdir -p "$OUT"
xcodebuild -create-xcframework \
	-library "$BUILD/device/libmotty.a" -headers "$BUILD/device/include" \
	-library "$BUILD/simulator/libmotty.a" -headers "$BUILD/simulator/include" \
	-output "$OUT/$NAME.xcframework"

echo "built $OUT/$NAME.xcframework"
