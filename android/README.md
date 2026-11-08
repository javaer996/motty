# Motty Android

Kotlin + Jetpack Compose 前端，复用仓库根目录的 Go 核心（`internal/mobilecore`），通过 JNI 调用 `libmotty.so`。

架构、协议与里程碑见 [`docs/android/architecture.md`](../docs/android/architecture.md)。

## 环境

- JDK 17
- Android SDK 35 与 build-tools 35.0.0
- Android NDK 27.2.12479018（用于编译 Go 原生库）
- Go 1.26

默认 SDK 路径：`/usr/local/share/android-commandlinetools`；可用 `ANDROID_HOME` 覆盖。

## 构建

```sh
# 1. 编译 Go 核心为 Android 共享库（每次改 Go 后重跑）
NDK_VERSION=27.2.12479018 scripts/build-go.sh

# 2. 构建并安装
./gradlew :app:assembleDebug
adb install -r app/build/outputs/apk/debug/app-debug.apk
```

`app/src/main/jniLibs/` 为生成目录，不提交；缺失时 Gradle 的 `checkGoCore` 任务会给出明确提示。

## 测试

```sh
./gradlew :app:testDebugUnitTest
./gradlew :app:connectedDebugAndroidTest   # 需要连接真机
```
