package dev.motty.app.data

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import java.io.File
import java.security.GeneralSecurityException
import java.security.KeyStore
import java.security.SecureRandom
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/** The saved secrets exist but the key that opens them is gone. */
class VaultKeyLostException(message: String) : Exception(message)

/**
 * Owns the 32-byte vault data key. The key itself is sealed with an
 * AndroidKeyStore AES-GCM key and kept in `vault.keywrap`; Go only ever sees
 * the unwrapped bytes at startup.
 *
 * Losing the Keystore key while `vault.json` exists is not recoverable, so it
 * raises [VaultKeyLostException] instead of silently replacing the vault key.
 */
class VaultKeyStore(private val context: Context) {
    private val alias = "motty.vault.wrap"
    private val aad = "motty.vault.key.v1".toByteArray(Charsets.US_ASCII)
    private val dir = File(context.filesDir, "motty")
    private val wrapperFile = File(dir, "vault.keywrap")
    private val legacyFile = File(dir, "vault.key")
    private val vaultFile = File(dir, "vault.json")

    /** Returns the 32-byte data key, creating and sealing one on first run. */
    fun obtain(): ByteArray {
        dir.mkdirs()
        if (wrapperFile.exists()) {
            return unwrap()
        }
        if (legacyFile.exists()) {
            val key = readLegacy()
            writeWrapper(key)
            legacyFile.delete()
            return key
        }
        if (vaultFile.exists()) {
            throw VaultKeyLostException(
                "the Android Keystore key is gone and the saved secrets cannot be opened",
            )
        }
        val key = ByteArray(KEY_SIZE)
        SecureRandom().nextBytes(key)
        writeWrapper(key)
        return key
    }

    /** Deletes the wrapper and its Keystore key. M1.1's recovery entry point. */
    fun reset() {
        wrapperFile.delete()
        legacyFile.delete()
        runCatching {
            val keyStore = KeyStore.getInstance(ANDROID_KEYSTORE).apply { load(null) }
            keyStore.deleteEntry(alias)
        }
    }

    private fun unwrap(): ByteArray {
        val raw = wrapperFile.readBytes()
        val headerSize = MAGIC.size + 1
        if (raw.size <= headerSize + 12 ||
            !raw.copyOfRange(0, MAGIC.size).contentEquals(MAGIC) ||
            raw[MAGIC.size].toInt() != VERSION
        ) {
            return recoverFromDamage()
        }
        val iv = raw.copyOfRange(headerSize, headerSize + 12)
        val ciphertext = raw.copyOfRange(headerSize + 12, raw.size)
        return try {
            val cipher = Cipher.getInstance(TRANSFORMATION)
            cipher.init(Cipher.DECRYPT_MODE, secretKey(), GCMParameterSpec(128, iv))
            cipher.updateAAD(aad)
            val key = cipher.doFinal(ciphertext)
            if (key.size != KEY_SIZE) throw GeneralSecurityException("bad key length")
            key
        } catch (_: GeneralSecurityException) {
            recoverFromDamage()
        }
    }

    /**
     * A damaged wrapper with no vault is harmless: there is nothing to open.
     * With a vault it means the key is gone, and guessing would destroy data.
     */
    private fun recoverFromDamage(): ByteArray {
        if (vaultFile.exists()) {
            throw VaultKeyLostException(
                "the saved secrets cannot be opened: the Android Keystore key is gone",
            )
        }
        wrapperFile.delete()
        val key = ByteArray(KEY_SIZE)
        SecureRandom().nextBytes(key)
        writeWrapper(key)
        return key
    }

    private fun readLegacy(): ByteArray {
        val text = legacyFile.readText().trim()
        val key = try {
            text.chunked(2).map { it.toInt(16).toByte() }.toByteArray()
        } catch (_: NumberFormatException) {
            ByteArray(0)
        }
        if (key.size != KEY_SIZE) {
            if (vaultFile.exists()) {
                throw VaultKeyLostException("the old vault key file is damaged")
            }
            val fresh = ByteArray(KEY_SIZE)
            SecureRandom().nextBytes(fresh)
            return fresh
        }
        return key
    }

    private fun writeWrapper(key: ByteArray) {
        val cipher = Cipher.getInstance(TRANSFORMATION)
        cipher.init(Cipher.ENCRYPT_MODE, secretKey())
        cipher.updateAAD(aad)
        val ciphertext = cipher.doFinal(key)
        val iv = cipher.iv
        val out = ByteArray(MAGIC.size + 1 + iv.size + ciphertext.size)
        MAGIC.copyInto(out, 0)
        out[MAGIC.size] = VERSION.toByte()
        iv.copyInto(out, MAGIC.size + 1)
        ciphertext.copyInto(out, MAGIC.size + 1 + iv.size)
        dir.mkdirs()
        val temp = File(dir, "vault.keywrap.tmp")
        temp.writeBytes(out)
        if (!temp.renameTo(wrapperFile)) {
            temp.copyTo(wrapperFile, overwrite = true)
            temp.delete()
        }
    }

    private fun secretKey(): SecretKey {
        val keyStore = KeyStore.getInstance(ANDROID_KEYSTORE).apply { load(null) }
        (keyStore.getKey(alias, null) as? SecretKey)?.let { return it }
        val generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, ANDROID_KEYSTORE)
        generator.init(
            KeyGenParameterSpec.Builder(
                alias,
                KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT,
            )
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256)
                .setUserAuthenticationRequired(false)
                .setRandomizedEncryptionRequired(true)
                .build(),
        )
        return generator.generateKey()
    }

    private companion object {
        const val ANDROID_KEYSTORE = "AndroidKeyStore"
        const val TRANSFORMATION = "AES/GCM/NoPadding"
        const val KEY_SIZE = 32
        const val VERSION = 1
        val MAGIC = "MTKY".toByteArray(Charsets.US_ASCII)
    }
}
