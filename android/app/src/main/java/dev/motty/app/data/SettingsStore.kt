package dev.motty.app.data

import android.content.Context
import androidx.appcompat.app.AppCompatDelegate
import androidx.core.os.LocaleListCompat

/**
 * Small persistent preferences. Only the language is stored for M1; it is
 * applied through AppCompat so the whole process switches without a restart.
 */
class SettingsStore(context: Context) {
    private val prefs = context.applicationContext
        .getSharedPreferences("motty.settings", Context.MODE_PRIVATE)

    /** Empty means follow the system; otherwise a BCP 47 tag. */
    var languageTag: String
        get() = prefs.getString(KEY_LANGUAGE, "").orEmpty()
        set(value) {
            prefs.edit().putString(KEY_LANGUAGE, value).apply()
            applyLanguage(value)
        }

    fun applySavedLanguage() {
        applyLanguage(languageTag)
    }

    private fun applyLanguage(tag: String) {
        val locales = if (tag.isEmpty()) {
            LocaleListCompat.getEmptyLocaleList()
        } else {
            LocaleListCompat.forLanguageTags(tag)
        }
        if (AppCompatDelegate.getApplicationLocales() != locales) {
            AppCompatDelegate.setApplicationLocales(locales)
        }
    }

    companion object {
        private const val KEY_LANGUAGE = "language"
    }
}
