#!/bin/sh
# Build the Go core as an Android shared library. The app's Gradle build never
# invokes Go: this script is the source of truth and works without Gradle.
#
#   ABIS=arm64-v8a scripts/build-go.sh
#   NDK_VERSION=27.2.12479018 scripts/build-go.sh
#
# Output: app/src/main/jniLibs/<abi>/libmotty.so (gitignored).
set -eu

ROOT=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
: "${ANDROID_HOME:=/usr/local/share/android-commandlinetools}"
NDK_VERSION=${NDK_VERSION:-27.2.12479018}
NDK=${ANDROID_NDK_HOME:-$ANDROID_HOME/ndk/$NDK_VERSION}
HOST=${NDK_HOST:-darwin-x86_64}
API=${ANDROID_API:-26}
ABIS=${ABIS:-arm64-v8a}
BUILD="$ROOT/android/.build/go"
OUT="$ROOT/android/app/src/main/jniLibs"

# 16 KB page alignment: NDK r27 does not default to it, and Play requires it
# for new apps. Keep the flags explicit even on a newer NDK.
CGO_LDFLAGS="-Wl,-z,max-page-size=16384 -Wl,-z,common-page-size=16384"

build_one() {
    abi=$1
    goarch=$2
    triple=$3
    cc="$NDK/toolchains/llvm/prebuilt/$HOST/bin/${triple}${API}-clang"
    if [ ! -x "$cc" ]; then
        echo "missing NDK compiler: $cc" >&2
        exit 1
    fi
    mkdir -p "$BUILD/$abi" "$OUT/$abi"
    echo "building $abi ($goarch) with $cc"
    (
        cd "$ROOT"
        CGO_ENABLED=1 GOOS=android GOARCH="$goarch" CC="$cc" \
            CGO_CFLAGS="-O2 -fPIC" \
            CGO_LDFLAGS="$CGO_LDFLAGS" \
            go build -buildmode=c-shared -trimpath \
                -ldflags="-s -w" \
                -o "$BUILD/$abi/libmotty.so" ./mobile
    )
    cp "$BUILD/$abi/libmotty.so" "$OUT/$abi/libmotty.so"
}

case "$ABIS" in
    arm64-v8a)
        build_one arm64-v8a arm64 aarch64-linux-android
        ;;
    x86_64)
        build_one x86_64 amd64 x86_64-linux-android
        ;;
    arm64-v8a,x86_64|arm64-v8a\ x86_64|x86_64,arm64-v8a|x86_64\ arm64-v8a)
        build_one arm64-v8a arm64 aarch64-linux-android
        build_one x86_64 amd64 x86_64-linux-android
        ;;
    *)
        echo "unsupported ABIS: $ABIS" >&2
        exit 1
        ;;
esac

echo "done: $OUT"
