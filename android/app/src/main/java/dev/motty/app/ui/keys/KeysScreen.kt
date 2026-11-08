package dev.motty.app.ui.keys

import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import dev.motty.app.R
import dev.motty.app.core.CoreRepository
import dev.motty.app.core.KeyInfo
import dev.motty.app.core.RpcException
import dev.motty.app.data.KeyImporter
import dev.motty.app.ui.components.ConfirmDialog
import dev.motty.app.ui.components.EmptyHint
import dev.motty.app.ui.components.MottyTopBar
import dev.motty.app.ui.theme.Elevated
import dev.motty.app.ui.theme.Mint
import dev.motty.app.ui.theme.Muted
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

private data class PendingImport(val name: String, val bytes: ByteArray)

@Composable
fun KeysScreen(repository: CoreRepository, onBack: () -> Unit) {
    val keys by repository.keys.collectAsState()
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    val importer = remember { KeyImporter(context) }
    var busy by remember { mutableStateOf(false) }
    var deleteTarget by remember { mutableStateOf<KeyInfo?>(null) }
    var overwriteTarget by remember { mutableStateOf<PendingImport?>(null) }
    var errorMessage by remember { mutableStateOf<String?>(null) }

    fun importKey(pending: PendingImport, overwrite: Boolean) {
        if (busy) return
        busy = true
        errorMessage = null
        scope.launch {
            try {
                repository.importKey(pending.name, pending.bytes, overwrite)
            } catch (error: RpcException) {
                if (error.code == "conflict" && !overwrite) {
                    overwriteTarget = pending
                } else {
                    errorMessage = error.message
                }
            } catch (error: Exception) {
                errorMessage = error.message
            } finally {
                busy = false
            }
        }
    }

    val picker = rememberLauncherForActivityResult(ActivityResultContracts.OpenDocument()) { uri ->
        if (uri == null) return@rememberLauncherForActivityResult
        scope.launch {
            val result = withContext(Dispatchers.IO) { importer.read(uri) }
            when (result) {
                is KeyImporter.Result.Success -> {
                    importKey(PendingImport(result.name, result.bytes), overwrite = false)
                }
                is KeyImporter.Result.Failed -> {
                    errorMessage = when (result.reason) {
                        KeyImporter.Failure.PUBLIC_KEY -> context.getString(R.string.unsupported_key)
                        KeyImporter.Failure.TOO_LARGE -> context.getString(R.string.import_failed, "1 MiB")
                        KeyImporter.Failure.EMPTY -> context.getString(R.string.import_failed, "empty")
                        KeyImporter.Failure.READ -> context.getString(R.string.import_failed, "I/O")
                    }
                }
            }
        }
    }

    Scaffold(
        containerColor = MaterialTheme.colorScheme.background,
        topBar = {
            MottyTopBar(
                title = stringResource(R.string.keys),
                onBack = onBack,
                actions = {
                    TextButton(onClick = { picker.launch(arrayOf("*/*")) }, enabled = !busy) {
                        Text(stringResource(R.string.import_key))
                    }
                },
            )
        },
    ) { padding ->
        LazyColumn(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding),
            contentPadding = PaddingValues(bottom = 24.dp),
            verticalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            item(key = "hint") {
                Text(
                    text = stringResource(R.string.keys_hint),
                    modifier = Modifier.padding(horizontal = 20.dp, vertical = 8.dp),
                    style = MaterialTheme.typography.bodySmall,
                    color = Muted,
                )
            }
            if (keys.isEmpty()) {
                item(key = "empty") { EmptyHint(stringResource(R.string.no_keys)) }
            }
            items(keys, key = { "k:${it.path}:${it.name}" }) { key ->
                KeyRow(key = key, onDelete = { deleteTarget = key })
            }
            errorMessage?.let { message ->
                item(key = "error") {
                    Text(
                        text = message,
                        modifier = Modifier.padding(horizontal = 20.dp, vertical = 8.dp),
                        color = MaterialTheme.colorScheme.error,
                        style = MaterialTheme.typography.bodySmall,
                    )
                }
            }
        }
    }

    deleteTarget?.let { key ->
        ConfirmDialog(
            title = stringResource(R.string.delete_key_confirm, key.name),
            text = key.fingerprint.ifEmpty { key.path },
            confirmLabel = stringResource(R.string.delete),
            destructive = true,
            onConfirm = {
                deleteTarget = null
                scope.launch {
                    try {
                        repository.deleteKey(key.name)
                    } catch (error: Exception) {
                        errorMessage = error.message
                    }
                }
            },
            onDismiss = { deleteTarget = null },
        )
    }

    overwriteTarget?.let { pending ->
        ConfirmDialog(
            title = pending.name,
            text = stringResource(R.string.overwrite_key),
            confirmLabel = stringResource(R.string.save),
            destructive = true,
            onConfirm = {
                overwriteTarget = null
                importKey(pending, overwrite = true)
            },
            onDismiss = { overwriteTarget = null },
        )
    }
}

@Composable
private fun KeyRow(key: KeyInfo, onDelete: () -> Unit) {
    Surface(
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 16.dp),
        shape = RoundedCornerShape(14.dp),
        color = Elevated,
    ) {
        Row(
            modifier = Modifier.padding(start = 16.dp, top = 12.dp, bottom = 12.dp, end = 4.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Box(
                modifier = Modifier
                    .size(34.dp)
                    .background(Mint.copy(alpha = 0.16f), CircleShape),
                contentAlignment = Alignment.Center,
            ) {
                Text("K", color = Mint, style = MaterialTheme.typography.titleMedium)
            }
            Spacer(Modifier.width(14.dp))
            Column(Modifier.weight(1f)) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(
                        text = key.name,
                        style = MaterialTheme.typography.bodyLarge,
                        color = MaterialTheme.colorScheme.onSurface,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                    )
                    if (key.encrypted) {
                        Spacer(Modifier.width(8.dp))
                        Text(
                            text = "passphrase",
                            style = MaterialTheme.typography.labelSmall,
                            color = Mint,
                        )
                    }
                }
                if (key.fingerprint.isNotEmpty()) {
                    Text(
                        text = key.fingerprint,
                        style = MaterialTheme.typography.labelSmall.copy(fontFamily = FontFamily.Monospace),
                        color = Muted,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                    )
                }
            }
            IconButton(onClick = onDelete) {
                Icon(
                    Icons.Default.Delete,
                    contentDescription = stringResource(R.string.delete),
                    tint = Muted,
                )
            }
        }
    }
}
