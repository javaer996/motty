package dev.motty.app.core

import android.content.Context
import android.util.Base64
import dev.motty.app.BuildConfig
import dev.motty.app.data.VaultKeyLostException
import dev.motty.app.data.VaultKeyStore
import java.io.File
import java.nio.ByteBuffer
import java.nio.ByteOrder
import java.util.concurrent.atomic.AtomicBoolean
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.serialization.ExperimentalSerializationApi
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.decodeFromJsonElement
import kotlinx.serialization.json.encodeToJsonElement
import kotlinx.serialization.json.put

/**
 * The process-wide view of the Go core. One instance per process; the
 * Application owns it so rotation never restarts the core or its sessions.
 */
@OptIn(ExperimentalSerializationApi::class)
class CoreRepository private constructor(private val context: Context) {
    private val client = CoreClient()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private val started = AtomicBoolean(false)
    private val json = Json {
        ignoreUnknownKeys = true
        explicitNulls = false
        encodeDefaults = false
    }

    private val _ready = MutableStateFlow(false)
    val ready: StateFlow<Boolean> = _ready.asStateFlow()

    private val _fatalError = MutableStateFlow<String?>(null)
    val fatalError: StateFlow<String?> = _fatalError.asStateFlow()

    private val _errorMessage = MutableStateFlow<String?>(null)
    val errorMessage: StateFlow<String?> = _errorMessage.asStateFlow()

    private val _info = MutableStateFlow<AppInfo?>(null)
    val info: StateFlow<AppInfo?> = _info.asStateFlow()

    private val _hosts = MutableStateFlow<List<Host>>(emptyList())
    val hosts: StateFlow<List<Host>> = _hosts.asStateFlow()

    private val _keys = MutableStateFlow<List<KeyInfo>>(emptyList())
    val keys: StateFlow<List<KeyInfo>> = _keys.asStateFlow()

    private val _sessions = MutableStateFlow<Map<Long, SessionInfo>>(emptyMap())
    val sessions: StateFlow<Map<Long, SessionInfo>> = _sessions.asStateFlow()

    private val _prompts = MutableStateFlow<Map<Long, PromptEvent>>(emptyMap())
    val prompts: StateFlow<Map<Long, PromptEvent>> = _prompts.asStateFlow()

    private val inputQueue = Channel<InputChunk>(Channel.UNLIMITED)
    private val resizeTargets = Channel<ResizeTarget>(Channel.CONFLATED)
    private val scrollTargets = Channel<ScrollTarget>(Channel.CONFLATED)
    private var eventBuffer = ByteBuffer.allocateDirect(32 * 1024).order(ByteOrder.LITTLE_ENDIAN)

    fun start() {
        if (!started.compareAndSet(false, true)) return
        scope.launch {
            try {
                val key = VaultKeyStore(context).obtain()
                try {
                    client.start(
                        dataDir = File(context.filesDir, "motty").absolutePath,
                        version = BuildConfig.VERSION_NAME,
                        vaultKey = key,
                    )
                } finally {
                    key.fill(0)
                }
                _info.value = callDecode("app.info")
                refreshHosts()
                refreshKeys()
                refreshSessions()
                _ready.value = true
                startWorkers()
            } catch (error: VaultKeyLostException) {
                _fatalError.value = error.message
            } catch (error: RpcException) {
                _fatalError.value = "${error.code}: ${error.message}"
            } catch (error: Throwable) {
                _fatalError.value = error.message ?: error.toString()
            }
        }
    }

    private fun startWorkers() {
        scope.launch { pollEvents() }
        scope.launch { drainInput() }
        scope.launch { drainResize() }
        scope.launch { drainScroll() }
    }

    // ---- RPC helpers ----

    private suspend inline fun <reified T> callDecode(
        method: String,
        params: JsonElement? = null,
    ): T = json.decodeFromJsonElement(client.call(method, params))

    private suspend fun callPing(method: String, params: JsonElement? = null) {
        client.call(method, params)
    }

    private fun obj(vararg pairs: Pair<String, JsonElement>): JsonElement =
        buildJsonObject { pairs.forEach { (key, value) -> put(key, value) } }

    private fun text(value: String): JsonElement = JsonPrimitive(value)
    private fun number(value: Number): JsonElement = JsonPrimitive(value)

    fun clearError() {
        _errorMessage.value = null
    }

    fun reportError(message: String?) {
        if (!message.isNullOrBlank()) _errorMessage.value = message
    }

    // ---- hosts ----

    suspend fun refreshHosts() {
        val payload: HostsPayload = callDecode("hosts.list")
        _hosts.value = payload.hosts.sortedWith(
            compareBy({ it.group.lowercase() }, { it.label.lowercase() }),
        )
    }

    suspend fun saveHost(host: Host): Host {
        val saved: Host = callDecode(
            "hosts.save",
            json.encodeToJsonElement(Host.serializer(), host),
        )
        refreshHosts()
        return saved
    }

    suspend fun deleteHost(id: String) {
        callPing("hosts.delete", obj("id" to text(id)))
        refreshHosts()
    }

    suspend fun passwordSaved(hostID: String): Boolean {
        val status: PasswordStatus = callDecode(
            "hosts.passwordStatus",
            obj("id" to text(hostID)),
        )
        return status.saved
    }

    suspend fun setHostPassword(hostID: String, password: String) {
        callPing(
            "hosts.password",
            obj("id" to text(hostID), "password" to text(password)),
        )
    }

    suspend fun clearHostPassword(hostID: String) {
        callPing(
            "hosts.password",
            obj("id" to text(hostID), "remove" to JsonPrimitive(true)),
        )
    }

    // ---- keys ----

    suspend fun refreshKeys() {
        val payload: KeysPayload = callDecode("keys.list")
        _keys.value = payload.keys
    }

    suspend fun importKey(name: String, bytes: ByteArray, overwrite: Boolean): KeyInfo {
        val encoded = Base64.encodeToString(bytes, Base64.NO_WRAP)
        val info: KeyInfo = callDecode(
            "keys.import",
            buildJsonObject {
                put("name", name)
                put("data", encoded)
                put("overwrite", overwrite)
            },
        )
        refreshKeys()
        return info
    }

    suspend fun deleteKey(name: String) {
        callPing("keys.delete", obj("name" to text(name)))
        refreshKeys()
    }

    // ---- sessions ----

    suspend fun refreshSessions() {
        val payload: SessionsPayload = callDecode("sessions.list")
        _sessions.value = payload.sessions.associateBy { it.id }
    }

    suspend fun openSession(hostID: String, cols: Int = 80, rows: Int = 24): SessionInfo {
        val info: SessionInfo = callDecode(
            "sessions.open",
            buildJsonObject {
                put("hostId", hostID)
                put("cols", cols)
                put("rows", rows)
            },
        )
        upsertSession(info)
        return info
    }

    suspend fun openLocalSession(cols: Int = 80, rows: Int = 24): SessionInfo {
        val info: SessionInfo = callDecode(
            "sessions.openLocal",
            buildJsonObject {
                put("cols", cols)
                put("rows", rows)
            },
        )
        upsertSession(info)
        return info
    }

    suspend fun closeSession(id: Long) {
        callPing("sessions.close", obj("id" to number(id)))
        // sessions.close pushes no event; remove it here.
        _sessions.value = _sessions.value - id
        _prompts.value = _prompts.value - id
    }

    suspend fun reconnectSession(id: Long): SessionInfo {
        val info: SessionInfo = callDecode("sessions.reconnect", obj("id" to number(id)))
        upsertSession(info)
        return info
    }

    suspend fun answer(id: Long, request: AnswerRequest) {
        callPing(
            "sessions.answer",
            buildJsonObject {
                put("id", id)
                put("kind", request.kind)
                put("value", request.value)
                put("save", request.save)
                put("answers", json.encodeToJsonElement(request.answers))
                put("reject", request.reject)
                put("skip", request.skip)
            },
        )
        _prompts.value = _prompts.value - id
    }

    /** Queues terminal input; chunks are sent in the order they were typed. */
    fun queueInput(id: Long, data: ByteArray) {
        if (data.isEmpty()) return
        inputQueue.trySend(InputChunk(id, data))
    }

    fun requestResize(id: Long, cols: Int, rows: Int) {
        if (cols < 1 || rows < 1) return
        resizeTargets.trySend(ResizeTarget(id, cols, rows))
    }

    /** Sends only the newest scroll position; obsolete targets are dropped. */
    fun requestScrollTo(id: Long, offset: Int) {
        scrollTargets.trySend(ScrollTarget(id, offset))
    }

    fun snapshot(id: Long, sinceVersion: Long, buffer: ByteBuffer): Int =
        client.screenSnapshot(id, sinceVersion, buffer)

    fun desktopFrame(id: Long, sinceVersion: Long, buffer: ByteBuffer): Int =
        client.desktopFrame(id, sinceVersion, buffer)

    // ---- workers ----

    private suspend fun drainInput() {
        for (chunk in inputQueue) {
            try {
                callPing(
                    "sessions.input",
                    buildJsonObject {
                        put("id", chunk.sessionID)
                        put(
                            "data",
                            Base64.encodeToString(chunk.data, Base64.NO_WRAP),
                        )
                    },
                )
            } catch (error: RpcException) {
                reportError(error.message)
            }
        }
    }

    private suspend fun drainResize() {
        for (target in resizeTargets) {
            try {
                callPing(
                    "sessions.resize",
                    buildJsonObject {
                        put("id", target.sessionID)
                        put("cols", target.cols)
                        put("rows", target.rows)
                    },
                )
            } catch (error: RpcException) {
                reportError(error.message)
            }
        }
    }

    private suspend fun drainScroll() {
        for (target in scrollTargets) {
            try {
                callPing(
                    "sessions.scrollTo",
                    buildJsonObject {
                        put("id", target.sessionID)
                        put("offset", target.offset)
                    },
                )
            } catch (error: RpcException) {
                reportError(error.message)
            }
        }
    }

    private suspend fun pollEvents() {
        while (currentCoroutineContext().isActive) {
            var activity = false
            while (true) {
                val n = client.pollEvent(eventBuffer)
                if (n > 0) {
                    handleEvent(eventBytes(n))
                    activity = true
                    continue
                }
                if (n < 0) {
                    val needed = -n
                    if (needed in 1..MAX_EVENT_BUFFER) {
                        eventBuffer = ByteBuffer
                            .allocateDirect(maxOf(needed, eventBuffer.capacity() * 2))
                            .order(ByteOrder.LITTLE_ENDIAN)
                    }
                }
                break
            }
            delay(if (activity || _sessions.value.isNotEmpty()) 100 else 500)
        }
    }

    private fun eventBytes(length: Int): ByteArray {
        val bytes = ByteArray(length)
        val copy = eventBuffer.duplicate()
        copy.position(0)
        copy.get(bytes, 0, length)
        return bytes
    }

    private fun handleEvent(bytes: ByteArray) {
        val event = try {
            json.decodeFromString(CoreEvent.serializer(), bytes.decodeToString())
        } catch (error: Exception) {
            // A dropped event must be visible: it can strand a session in a
            // prompt phase with no way to answer.
            android.util.Log.w("MottyCore", "drop undecodable event", error)
            return
        }
        when (event.type) {
            "session.state" -> event.session?.let { upsertSession(it) }
            "session.prompt" -> {
                val id = event.sessionID ?: return
                _prompts.value = _prompts.value + (
                    id to PromptEvent(
                        sessionID = id,
                        kind = event.kind,
                        questions = event.questions,
                        attempt = event.attempt,
                        hostKey = event.hostKey,
                        identityPath = event.identityPath,
                    )
                    )
            }
        }
    }

    private fun upsertSession(info: SessionInfo) {
        _sessions.value = _sessions.value + (info.id to info)
        if (info.phase == "failed" || info.phase == "closed") {
            _prompts.value = _prompts.value - info.id
        }
    }

    private fun stop() {
        scope.cancel()
        client.close()
    }

    private data class InputChunk(val sessionID: Long, val data: ByteArray)
    private data class ResizeTarget(val sessionID: Long, val cols: Int, val rows: Int)
    private data class ScrollTarget(val sessionID: Long, val offset: Int)

    companion object {
        private const val MAX_EVENT_BUFFER = 8 shl 20

        @Volatile
        private var instance: CoreRepository? = null

        fun get(context: Context): CoreRepository =
            instance ?: synchronized(this) {
                instance ?: CoreRepository(context.applicationContext).also { instance = it }
            }
    }
}
