package dev.motty.app.ui.terminal

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.Checkbox
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.rememberModalBottomSheetState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp
import dev.motty.app.R
import dev.motty.app.core.AnswerRequest
import dev.motty.app.core.PromptEvent
import dev.motty.app.ui.theme.ErrorRed
import dev.motty.app.ui.theme.Mint
import dev.motty.app.ui.theme.Muted

/**
 * The sheet that answers a connection waiting for the user: a host key, a
 * password, a key passphrase, or keyboard-interactive questions.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun PromptSheet(
    prompt: PromptEvent,
    onAnswer: (AnswerRequest) -> Unit,
    onDismiss: () -> Unit,
) {
    val sheetState = rememberModalBottomSheetState(skipPartiallyExpanded = true)
    ModalBottomSheet(
        onDismissRequest = onDismiss,
        sheetState = sheetState,
        containerColor = MaterialTheme.colorScheme.surface,
    ) {
        Column(
            modifier = Modifier
                .fillMaxWidth()
                .imePadding()
                .navigationBarsPadding()
                .verticalScroll(rememberScrollState())
                .padding(horizontal = 20.dp)
                .padding(bottom = 24.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            when (prompt.kind) {
                "hostkey" -> HostKeyContent(prompt, onAnswer, onDismiss)
                "passphrase" -> SecretContent(
                    title = stringResource(R.string.phase_passphrase),
                    prompt = prompt,
                    identityPath = prompt.identityPath,
                    onAnswer = onAnswer,
                )
                else -> SecretContent(
                    title = stringResource(R.string.phase_password),
                    prompt = prompt,
                    identityPath = "",
                    onAnswer = onAnswer,
                )
            }
        }
    }
}

@Composable
private fun HostKeyContent(
    prompt: PromptEvent,
    onAnswer: (AnswerRequest) -> Unit,
    onDismiss: () -> Unit,
) {
    val key = prompt.hostKey
    if (key == null) {
        Text(stringResource(R.string.error_generic))
        TextButton(onClick = onDismiss) { Text(stringResource(R.string.cancel)) }
        return
    }
    var rememberKey by remember(prompt) { mutableStateOf(!key.changed) }

    Text(
        text = stringResource(if (key.changed) R.string.host_key_changed else R.string.host_key_title),
        style = MaterialTheme.typography.titleMedium,
        color = if (key.changed) ErrorRed else MaterialTheme.colorScheme.onSurface,
    )
    Text(
        text = key.address,
        fontFamily = FontFamily.Monospace,
        style = MaterialTheme.typography.bodyMedium,
    )
    Text(
        text = "${key.type}  ${key.fingerprint}",
        fontFamily = FontFamily.Monospace,
        style = MaterialTheme.typography.bodySmall,
        color = Muted,
    )
    val known = key.known.orEmpty()
    if (known.isNotEmpty()) {
        Text(
            text = known.joinToString(", "),
            fontFamily = FontFamily.Monospace,
            style = MaterialTheme.typography.labelSmall,
            color = Muted,
        )
    }
    if (!key.changed) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Checkbox(checked = rememberKey, onCheckedChange = { rememberKey = it })
            Text(stringResource(R.string.trust_and_save))
        }
    }
    Row(
        modifier = Modifier.fillMaxWidth(),
        horizontalArrangement = Arrangement.spacedBy(12.dp, Alignment.End),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        TextButton(
            onClick = {
                onAnswer(AnswerRequest(kind = "hostkey", reject = true))
            },
        ) {
            Text(stringResource(R.string.reject), color = ErrorRed)
        }
        Button(
            onClick = {
                onAnswer(AnswerRequest(kind = "hostkey", save = rememberKey))
            },
        ) {
            Text(stringResource(R.string.trust))
        }
    }
}

@Composable
private fun SecretContent(
    title: String,
    prompt: PromptEvent,
    identityPath: String,
    onAnswer: (AnswerRequest) -> Unit,
) {
    val questions = prompt.questions
    val count = questions.size.coerceAtLeast(1)
    var value by remember(prompt) { mutableStateOf("") }
    val answers = remember(prompt) { mutableStateListOf<String>().apply { repeat(count) { add("") } } }
    var saveSecret by remember(prompt) { mutableStateOf(false) }
    val canSave = canSaveSecret(prompt)

    Text(title, style = MaterialTheme.typography.titleMedium)
    if (identityPath.isNotEmpty()) {
        Text(
            text = identityPath,
            fontFamily = FontFamily.Monospace,
            style = MaterialTheme.typography.bodySmall,
            color = Muted,
        )
    }
    if (questions.isEmpty()) {
        SecretField(
            label = stringResource(R.string.password),
            value = value,
            onValueChange = { value = it },
        )
    } else {
        questions.forEachIndexed { index, question ->
            val label = question.text.ifBlank {
                stringResource(R.string.password)
            }
            SecretField(
                label = label,
                value = answers[index],
                secret = question.secret,
                onValueChange = { answers[index] = it },
            )
        }
    }
    if (prompt.attempt > 1) {
        Text(
            text = stringResource(R.string.previous_refused),
            style = MaterialTheme.typography.bodySmall,
            color = ErrorRed,
        )
    }
    if (canSave) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Checkbox(checked = saveSecret, onCheckedChange = { saveSecret = it })
            Text(stringResource(R.string.save_password))
        }
    }
    Row(
        modifier = Modifier.fillMaxWidth(),
        horizontalArrangement = Arrangement.End,
    ) {
        Button(onClick = {
            when {
                prompt.kind == "passphrase" -> {
                    onAnswer(AnswerRequest(kind = "passphrase", value = value))
                }

                isPlainPassword(prompt) -> {
                    val answer = questions.firstOrNull()?.let { answers[0] } ?: value
                    onAnswer(
                        AnswerRequest(
                            kind = "password",
                            value = answer,
                            save = saveSecret,
                        ),
                    )
                }

                else -> {
                    onAnswer(AnswerRequest(kind = "questions", answers = answers.toList()))
                }
            }
        }) {
            Text(stringResource(R.string.answer))
        }
    }
}

@Composable
private fun SecretField(
    label: String,
    value: String,
    secret: Boolean = true,
    onValueChange: (String) -> Unit,
) {
    OutlinedTextField(
        value = value,
        onValueChange = onValueChange,
        label = { Text(label) },
        singleLine = true,
        modifier = Modifier.fillMaxWidth(),
        visualTransformation = if (secret) PasswordVisualTransformation() else androidx.compose.ui.text.input.VisualTransformation.None,
        keyboardOptions = KeyboardOptions(
            keyboardType = if (secret) KeyboardType.Password else KeyboardType.Text,
            autoCorrect = false,
        ),
    )
}

/** The one plain-password question the core is willing to keep for the host. */
private fun canSaveSecret(prompt: PromptEvent): Boolean {
    if (prompt.kind != "password" || prompt.attempt > 1) return false
    val questions = prompt.questions
    if (questions.isEmpty()) return true
    if (questions.size != 1 || !questions[0].secret) return false
    val text = questions[0].text.lowercase()
    return text.isEmpty() || text.contains("password") ||
        text.contains("密码") || text.contains("密碼")
}

private fun isPlainPassword(prompt: PromptEvent): Boolean = canSaveSecret(prompt)
