package dev.motty.app.ui.settings

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.RadioButton
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import dev.motty.app.R
import dev.motty.app.core.CoreRepository
import dev.motty.app.data.SettingsStore
import dev.motty.app.ui.components.DetailRow
import dev.motty.app.ui.components.MottyTopBar
import dev.motty.app.ui.components.SectionHeader

private val LANGUAGES = listOf(
    "" to R.string.language_system,
    "en" to R.string.language_english,
    "zh-Hans" to R.string.language_simplified,
    "zh-Hant" to R.string.language_traditional,
)

@Composable
fun SettingsScreen(repository: CoreRepository, onBack: () -> Unit) {
    val context = LocalContext.current
    val store = remember { SettingsStore(context) }
    val info by repository.info.collectAsState()
    var selected = remember { store.languageTag }

    Scaffold(
        containerColor = MaterialTheme.colorScheme.background,
        topBar = {
            MottyTopBar(title = stringResource(R.string.settings), onBack = onBack)
        },
    ) { padding ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding)
                .verticalScroll(rememberScrollState()),
        ) {
            SectionHeader(stringResource(R.string.language))
            LANGUAGES.forEach { (tag, label) ->
                Row(
                    modifier = Modifier
                        .fillMaxWidth()
                        .clickable {
                            selected = tag
                            store.languageTag = tag
                        }
                        .padding(horizontal = 20.dp, vertical = 10.dp),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    RadioButton(
                        selected = selected == tag,
                        onClick = {
                            selected = tag
                            store.languageTag = tag
                        },
                    )
                    Text(
                        text = stringResource(label),
                        modifier = Modifier.padding(start = 8.dp),
                        style = MaterialTheme.typography.bodyLarge,
                        color = MaterialTheme.colorScheme.onBackground,
                    )
                }
            }

            SectionHeader(stringResource(R.string.version))
            info?.let { appInfo ->
                DetailRow(stringResource(R.string.version), appInfo.version)
                DetailRow(stringResource(R.string.platform), appInfo.platform)
                DetailRow(stringResource(R.string.go_version), appInfo.goVersion)
            }
        }
    }
}
