package dev.motty.app.ui.hosts

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Checkbox
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp
import dev.motty.app.R
import dev.motty.app.core.CoreRepository
import dev.motty.app.core.Host
import dev.motty.app.ui.components.MottyTopBar
import dev.motty.app.ui.theme.Muted
import kotlinx.coroutines.launch

@Composable
fun HostEditorScreen(
    repository: CoreRepository,
    hostID: String?,
    onBack: () -> Unit,
) {
    val hosts by repository.hosts.collectAsState()
    val keys by repository.keys.collectAsState()
    val base = remember(hostID, hosts) { hosts.firstOrNull { it.id == hostID } }
    val scope = rememberCoroutineScope()

    var label by remember(base?.id) { mutableStateOf(base?.label.orEmpty()) }
    var address by remember(base?.id) { mutableStateOf(base?.address.orEmpty()) }
    var port by remember(base?.id) { mutableStateOf((base?.port?.takeIf { it != 0 } ?: 22).toString()) }
    var user by remember(base?.id) { mutableStateOf(base?.user.orEmpty()) }
    var group by remember(base?.id) { mutableStateOf(base?.group.orEmpty()) }
    var auth by remember(base?.id) { mutableStateOf(base?.auth.orEmpty()) }
    var identity by remember(base?.id) { mutableStateOf(base?.identity.orEmpty()) }
    var proxyJump by remember(base?.id) { mutableStateOf(base?.proxyJump.orEmpty()) }
    var password by remember(base?.id) { mutableStateOf("") }
    var savePassword by remember(base?.id) { mutableStateOf(false) }
    var hasSavedPassword by remember(base?.id) { mutableStateOf(false) }
    var busy by remember { mutableStateOf(false) }
    var errorMessage by remember { mutableStateOf<String?>(null) }

    LaunchedEffect(base?.id) {
        val id = base?.id ?: return@LaunchedEffect
        try {
            hasSavedPassword = repository.passwordSaved(id)
        } catch (_: Exception) {
            hasSavedPassword = false
        }
    }

    val portNumber = port.toIntOrNull()
    val valid = label.isNotBlank() && address.isNotBlank() && user.isNotBlank() &&
        portNumber != null && portNumber in 1..65535

    fun save() {
        if (!valid || busy) return
        busy = true
        errorMessage = null
        scope.launch {
            try {
                val updated = (base ?: Host()).copy(
                    label = label.trim(),
                    address = address.trim(),
                    port = portNumber ?: 22,
                    user = user.trim(),
                    group = group.trim(),
                    auth = auth,
                    identity = identity,
                    proxyJump = proxyJump.trim(),
                )
                val saved = repository.saveHost(updated)
                if (password.isNotEmpty() && savePassword) {
                    repository.setHostPassword(saved.id, password)
                }
                onBack()
            } catch (error: Exception) {
                errorMessage = error.message
            } finally {
                busy = false
            }
        }
    }

    Scaffold(
        containerColor = MaterialTheme.colorScheme.background,
        topBar = {
            MottyTopBar(
                title = stringResource(if (base == null) R.string.add_host else R.string.edit_host),
                onBack = onBack,
                actions = {
                    TextButton(onClick = { save() }, enabled = valid && !busy) {
                        if (busy) {
                            CircularProgressIndicator(
                                modifier = Modifier.padding(4.dp),
                                strokeWidth = 2.dp,
                            )
                        } else {
                            Text(stringResource(R.string.save))
                        }
                    }
                },
            )
        },
    ) { padding ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding)
                .verticalScroll(rememberScrollState())
                .padding(horizontal = 16.dp, vertical = 8.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            OutlinedTextField(
                value = label,
                onValueChange = { label = it },
                label = { Text(stringResource(R.string.label)) },
                singleLine = true,
                modifier = Modifier.fillMaxWidth(),
            )
            OutlinedTextField(
                value = address,
                onValueChange = { address = it },
                label = { Text(stringResource(R.string.address)) },
                singleLine = true,
                keyboardOptions = KeyboardOptions(
                    keyboardType = KeyboardType.Uri,
                    autoCorrect = false,
                ),
                textStyle = MaterialTheme.typography.bodyLarge.copy(fontFamily = FontFamily.Monospace),
                modifier = Modifier.fillMaxWidth(),
            )
            Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                OutlinedTextField(
                    value = port,
                    onValueChange = { value ->
                        port = value.filter { it.isDigit() }.take(5)
                    },
                    label = { Text(stringResource(R.string.port)) },
                    singleLine = true,
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number),
                    modifier = Modifier.weight(1f),
                )
                OutlinedTextField(
                    value = user,
                    onValueChange = { user = it },
                    label = { Text(stringResource(R.string.user)) },
                    singleLine = true,
                    keyboardOptions = KeyboardOptions(autoCorrect = false),
                    textStyle = MaterialTheme.typography.bodyLarge.copy(fontFamily = FontFamily.Monospace),
                    modifier = Modifier.weight(1f),
                )
            }
            OutlinedTextField(
                value = group,
                onValueChange = { group = it },
                label = { Text(stringResource(R.string.group)) },
                singleLine = true,
                modifier = Modifier.fillMaxWidth(),
            )
            PickerField(
                label = stringResource(R.string.authentication),
                value = authLabel(auth),
                options = listOf(
                    "" to stringResource(R.string.auth_automatic),
                    "password" to stringResource(R.string.auth_password),
                    "key" to stringResource(R.string.auth_key),
                    "agent" to stringResource(R.string.auth_agent),
                ),
                onSelect = { auth = it },
            )
            if (auth == "key" || auth.isEmpty()) {
                PickerField(
                    label = stringResource(R.string.identity),
                    value = keys.firstOrNull { it.path == identity }?.name
                        ?: stringResource(R.string.auth_automatic),
                    options = listOf("" to stringResource(R.string.auth_automatic)) +
                        keys.map { it.path to it.name },
                    onSelect = { identity = it },
                )
            }
            OutlinedTextField(
                value = proxyJump,
                onValueChange = { proxyJump = it },
                label = { Text(stringResource(R.string.proxy_jump)) },
                singleLine = true,
                keyboardOptions = KeyboardOptions(autoCorrect = false),
                textStyle = MaterialTheme.typography.bodyLarge.copy(fontFamily = FontFamily.Monospace),
                modifier = Modifier.fillMaxWidth(),
            )

            if (base != null && hasSavedPassword) {
                Text(
                    text = stringResource(R.string.password_saved),
                    style = MaterialTheme.typography.bodySmall,
                    color = Muted,
                )
            }
            OutlinedTextField(
                value = password,
                onValueChange = { password = it },
                label = { Text(stringResource(R.string.password_optional)) },
                singleLine = true,
                visualTransformation = PasswordVisualTransformation(),
                keyboardOptions = KeyboardOptions(
                    keyboardType = KeyboardType.Password,
                    autoCorrect = false,
                ),
                modifier = Modifier.fillMaxWidth(),
            )
            Row(
                verticalAlignment = Alignment.CenterVertically,
                modifier = Modifier
                    .fillMaxWidth()
                    .clickable { savePassword = !savePassword },
            ) {
                Checkbox(checked = savePassword, onCheckedChange = { savePassword = it })
                Text(stringResource(R.string.save_password))
            }
            if (base != null && hasSavedPassword) {
                TextButton(
                    onClick = {
                        scope.launch {
                            try {
                                repository.clearHostPassword(base.id)
                                hasSavedPassword = false
                            } catch (error: Exception) {
                                errorMessage = error.message
                            }
                        }
                    },
                ) {
                    Text(
                        text = stringResource(R.string.clear_password),
                        color = MaterialTheme.colorScheme.error,
                    )
                }
            }
            errorMessage?.let { message ->
                Text(text = message, color = MaterialTheme.colorScheme.error)
            }
            Box(Modifier.padding(bottom = 24.dp))
        }
    }
}

@Composable
private fun authLabel(auth: String): String = when (auth) {
    "password" -> stringResource(R.string.auth_password)
    "key" -> stringResource(R.string.auth_key)
    "agent" -> stringResource(R.string.auth_agent)
    else -> stringResource(R.string.auth_automatic)
}

@Composable
private fun PickerField(
    label: String,
    value: String,
    options: List<Pair<String, String>>,
    onSelect: (String) -> Unit,
) {
    var open by remember { mutableStateOf(false) }
    Box {
        OutlinedButton(
            onClick = { open = true },
            modifier = Modifier.fillMaxWidth(),
        ) {
            Column(
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(vertical = 4.dp),
                horizontalAlignment = Alignment.Start,
            ) {
                Text(
                    text = label,
                    style = MaterialTheme.typography.labelSmall,
                    color = Muted,
                )
                Text(text = value, style = MaterialTheme.typography.bodyLarge)
            }
        }
        DropdownMenu(expanded = open, onDismissRequest = { open = false }) {
            options.forEach { (key, text) ->
                DropdownMenuItem(
                    text = { Text(text) },
                    onClick = {
                        open = false
                        onSelect(key)
                    },
                )
            }
        }
    }
}

