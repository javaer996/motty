package dev.motty.app.core

import java.nio.ByteBuffer
import java.util.concurrent.Executors
import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.withContext
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonElement

@Serializable
data class RpcError(val code: String, val message: String)

@Serializable
private data class RpcEnvelope(
    val ok: Boolean,
    val result: JsonElement? = null,
    val error: RpcError? = null,
)

class RpcException(val code: String, message: String) : Exception(message)

/**
 * JSON-RPC over the JNI bridge. Control calls run one at a time on a dedicated
 * thread: a non-idempotent method is never executed concurrently, and the
 * caller's coroutine is suspended instead of blocking the UI.
 */
class CoreClient {
    private val control = Executors.newSingleThreadExecutor { runnable ->
        Thread(runnable, "motty-core")
    }.asCoroutineDispatcher()

    private val json = Json {
        ignoreUnknownKeys = true
        explicitNulls = false
        encodeDefaults = false
    }

    suspend fun start(dataDir: String, version: String, vaultKey: ByteArray): JsonElement =
        withContext(control) {
            val response = MottyNative.start(
                dataDir.toByteArray(Charsets.UTF_8),
                version.toByteArray(Charsets.UTF_8),
                vaultKey,
            )
            decode(response)
        }

    suspend fun call(method: String, params: JsonElement? = null): JsonElement =
        withContext(control) {
            val response = MottyNative.call(
                method.toByteArray(Charsets.US_ASCII),
                params?.toString()?.toByteArray(Charsets.UTF_8) ?: ByteArray(0),
            )
            decode(response)
        }

    fun pollEvent(buffer: ByteBuffer): Int = MottyNative.pollEvent(buffer)

    fun screenSnapshot(sessionId: Long, sinceVersion: Long, buffer: ByteBuffer): Int =
        MottyNative.screenSnapshot(sessionId, sinceVersion, buffer)

    fun desktopFrame(desktopId: Long, sinceVersion: Long, buffer: ByteBuffer): Int =
        MottyNative.desktopFrame(desktopId, sinceVersion, buffer)

    /** Runs on the control thread so no RPC can race the teardown. */
    suspend fun stop() = withContext(control) {
        MottyNative.stop()
    }

    fun close() {
        control.close()
    }

    private fun decode(response: ByteArray): JsonElement {
        if (response.isEmpty()) {
            throw RpcException("internal", "empty native response")
        }
        val envelope = try {
            json.decodeFromString(RpcEnvelope.serializer(), response.decodeToString())
        } catch (error: Exception) {
            throw RpcException("internal", "cannot decode the native response: ${error.message}")
        }
        if (!envelope.ok) {
            val error = envelope.error
            throw RpcException(error?.code ?: "internal", error?.message ?: "unknown failure")
        }
        return envelope.result ?: throw RpcException("internal", "the response has no result")
    }
}
