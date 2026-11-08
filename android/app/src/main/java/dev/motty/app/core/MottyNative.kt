package dev.motty.app.core

import java.nio.ByteBuffer

/**
 * The JNI surface of libmotty.so. Payloads are byte arrays, not strings: JNI
 * modified UTF-8 would corrupt emoji and other supplementary characters.
 */
internal object MottyNative {
    init {
        System.loadLibrary("motty")
    }

    external fun start(dataDir: ByteArray, version: ByteArray, vaultKey: ByteArray): ByteArray
    external fun call(method: ByteArray, params: ByteArray): ByteArray
    external fun pollEvent(buffer: ByteBuffer): Int
    external fun screenSnapshot(sessionId: Long, sinceVersion: Long, buffer: ByteBuffer): Int
    external fun desktopFrame(desktopId: Long, sinceVersion: Long, buffer: ByteBuffer): Int
    external fun stop()
}
