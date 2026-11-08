package dev.motty.app.data

import android.content.Context
import android.net.Uri
import android.provider.OpenableColumns
import java.io.ByteArrayOutputStream

/**
 * Reads a private key chosen through the storage access framework. The bytes
 * go straight to the Go core, which parses them before they ever touch disk;
 * nothing here inspects the key.
 */
class KeyImporter(private val context: Context) {
    enum class Failure { PUBLIC_KEY, TOO_LARGE, EMPTY, READ }

    sealed interface Result {
        data class Success(val name: String, val bytes: ByteArray) : Result
        data class Failed(val reason: Failure) : Result
    }

    fun read(uri: Uri): Result {
        val name = displayName(uri)
        if (name.endsWith(".pub", ignoreCase = true)) {
            return Result.Failed(Failure.PUBLIC_KEY)
        }
        val bytes = try {
            context.contentResolver.openInputStream(uri)?.use { input ->
                val output = ByteArrayOutputStream()
                val chunk = ByteArray(16 * 1024)
                var total = 0
                while (true) {
                    val count = input.read(chunk)
                    if (count < 0) break
                    total += count
                    if (total > MAX_KEY_BYTES) return Result.Failed(Failure.TOO_LARGE)
                    output.write(chunk, 0, count)
                }
                output.toByteArray()
            }
        } catch (_: Exception) {
            null
        } ?: return Result.Failed(Failure.READ)
        if (bytes.isEmpty()) return Result.Failed(Failure.EMPTY)
        return Result.Success(name.take(128), bytes)
    }

    private fun displayName(uri: Uri): String {
        try {
            context.contentResolver.query(uri, null, null, null, null)?.use { cursor ->
                val index = cursor.getColumnIndex(OpenableColumns.DISPLAY_NAME)
                if (index >= 0 && cursor.moveToFirst()) {
                    val value = cursor.getString(index)
                    if (!value.isNullOrBlank()) return value.substringAfterLast('/')
                }
            }
        } catch (_: Exception) {
            // Fall through to the path's last segment.
        }
        return uri.lastPathSegment?.substringAfterLast('/') ?: "key"
    }

    companion object {
        private const val MAX_KEY_BYTES = 1 shl 20
    }
}
