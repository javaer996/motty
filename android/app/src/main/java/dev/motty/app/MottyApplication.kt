package dev.motty.app

import android.app.Application
import dev.motty.app.core.CoreRepository
import dev.motty.app.data.SettingsStore

class MottyApplication : Application() {
    val repository: CoreRepository by lazy { CoreRepository.get(this) }

    override fun onCreate() {
        super.onCreate()
        SettingsStore(this).applySavedLanguage()
        repository.start()
    }
}
