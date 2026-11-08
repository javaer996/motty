package dev.motty.app.core

import kotlinx.serialization.Serializable

@Serializable
data class AppInfo(
    val version: String = "",
    val dataDir: String = "",
    val platform: String = "",
    val goVersion: String = "",
    val startedAt: Long = 0,
    val eventsDropped: Long = 0,
    val localShell: Boolean = false,
)

@Serializable
data class HostProxy(
    val kind: String = "",
    val address: String = "",
    val user: String = "",
    val bypass: String = "",
)

/**
 * Every field of the Go host model is here, including the desktop catalog's:
 * `hosts.save` patches the stored record with what it receives and removes
 * fields that arrive empty, so omitting one would silently delete it.
 */
@Serializable
data class Host(
    val id: String = "",
    val label: String = "",
    val group: String = "",
    val tags: List<String> = emptyList(),
    val icon: String = "",
    val color: String = "",
    val address: String = "",
    val user: String = "",
    val port: Int = 0,
    val protocol: String = "",
    val auth: String = "",
    val identity: String = "",
    val proxyJump: String = "",
    val loginCommand: String = "",
    val noHistory: Boolean = false,
    val viaSsh: String = "",
    val clipboard: Boolean? = null,
    val console: Boolean = false,
    val scale: Double = 0.0,
    val proxy: HostProxy? = null,
    val createdAt: Long = 0,
    val updatedAt: Long = 0,
    val system: String = "",
    val line: String = "",
    val flow: String = "",
    val algorithms: String = "",
    val algorithmList: String = "",
    val forwardX11: Boolean = false,
    val agentForward: Boolean = false,
    val transfer: String = "",
    val chromeless: Boolean = false,
)

@Serializable
data class KeyInfo(
    val name: String = "",
    val path: String = "",
    val publicKey: String = "",
    val fingerprint: String = "",
    val encrypted: Boolean = false,
    val createdAt: Long = 0,
)

@Serializable
data class SessionInfo(
    val id: Long = 0,
    val hostId: String = "",
    val title: String = "",
    val phase: String = "",
    val address: String = "",
    val user: String = "",
    val cols: Int = 0,
    val rows: Int = 0,
    val connectedAt: Long = 0,
    val error: String = "",
    val method: String = "",
    val serverVersion: String = "",
    val local: Boolean = false,
)

@Serializable
data class PromptQuestion(
    val text: String = "",
    val secret: Boolean = false,
)

@Serializable
data class HostKeyInfo(
    val address: String = "",
    val type: String = "",
    val fingerprint: String = "",
    // nil for a host that has no known_hosts entry yet; the Go event encodes
    // the empty list as null in older builds.
    val known: List<String>? = null,
    val file: String = "",
    val changed: Boolean = false,
)

@Serializable
data class CoreEvent(
    val type: String = "",
    val session: SessionInfo? = null,
    val sessionID: Long? = null,
    val kind: String = "",
    val questions: List<PromptQuestion> = emptyList(),
    val attempt: Int = 0,
    val hostKey: HostKeyInfo? = null,
    val identityPath: String = "",
    val error: String = "",
)

data class PromptEvent(
    val sessionID: Long,
    val kind: String,
    val questions: List<PromptQuestion> = emptyList(),
    val attempt: Int = 0,
    val hostKey: HostKeyInfo? = null,
    val identityPath: String = "",
)

@Serializable
data class AnswerRequest(
    val kind: String,
    val value: String = "",
    val save: Boolean = false,
    val answers: List<String> = emptyList(),
    val reject: Boolean = false,
    val skip: Boolean = false,
)

@Serializable
internal data class HostsPayload(val hosts: List<Host> = emptyList())

@Serializable
internal data class KeysPayload(val keys: List<KeyInfo> = emptyList())

@Serializable
internal data class SessionsPayload(val sessions: List<SessionInfo> = emptyList())

@Serializable
internal data class PasswordStatus(val saved: Boolean = false)
