package dev.motty.app.ui.terminal

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
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
import androidx.compose.ui.viewinterop.AndroidView
import dev.motty.app.R
import dev.motty.app.core.CoreRepository
import dev.motty.app.core.SessionInfo
import dev.motty.app.ui.components.ConfirmDialog
import dev.motty.app.ui.components.MottyTopBar
import dev.motty.app.ui.components.PhaseLabel
import dev.motty.app.ui.theme.Elevated
import dev.motty.app.ui.theme.ErrorRed
import dev.motty.app.ui.theme.Muted
import kotlinx.coroutines.launch

@Composable
fun SessionScreen(
    repository: CoreRepository,
    sessionID: Long,
    onBack: () -> Unit,
) {
    val sessions by repository.sessions.collectAsState()
    val prompts by repository.prompts.collectAsState()
    val session = sessions[sessionID]
    val prompt = prompts[sessionID]
    val scope = rememberCoroutineScope()
    val context = LocalContext.current

    var terminalView by remember { mutableStateOf<TerminalView?>(null) }
    var appCursorKeys by remember { mutableStateOf(false) }
    var menuOpen by remember { mutableStateOf(false) }
    var closeConfirm by remember { mutableStateOf(false) }
    var promptVisible by remember(prompt) { mutableStateOf(true) }

    if (session == null) {
        Scaffold(
            containerColor = MaterialTheme.colorScheme.background,
            topBar = { MottyTopBar(title = stringResource(R.string.sessions), onBack = onBack) },
        ) { padding ->
            Box(
                modifier = Modifier
                    .fillMaxSize()
                    .padding(padding),
                contentAlignment = Alignment.Center,
            ) {
                Text(stringResource(R.string.session_not_found), color = Muted)
            }
        }
        return
    }

    val title = when {
        session.local -> context.getString(R.string.local_shell)
        session.title.isNotEmpty() -> session.title
        session.address.isNotEmpty() -> session.address
        else -> stringResource(R.string.sessions)
    }

    Scaffold(
        containerColor = MaterialTheme.colorScheme.background,
        topBar = {
            MottyTopBar(
                title = title,
                onBack = onBack,
                actions = {
                    Box {
                        IconButton(onClick = { menuOpen = true }) {
                            Icon(
                                Icons.Default.MoreVert,
                                contentDescription = null,
                                tint = MaterialTheme.colorScheme.onBackground,
                            )
                        }
                        DropdownMenu(expanded = menuOpen, onDismissRequest = { menuOpen = false }) {
                            if (session.phase == "failed" || session.phase == "closed") {
                                DropdownMenuItem(
                                    text = { Text(stringResource(R.string.reconnect)) },
                                    onClick = {
                                        menuOpen = false
                                        scope.launch {
                                            try {
                                                repository.reconnectSession(session.id)
                                            } catch (error: Exception) {
                                                repository.reportError(error.message)
                                            }
                                        }
                                    },
                                )
                            }
                            DropdownMenuItem(
                                text = {
                                    Text(
                                        stringResource(R.string.close),
                                        color = MaterialTheme.colorScheme.error,
                                    )
                                },
                                onClick = {
                                    menuOpen = false
                                    closeConfirm = true
                                },
                            )
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
                .imePadding(),
        ) {
            SessionStatus(session = session, onReconnect = {
                scope.launch {
                    try {
                        repository.reconnectSession(session.id)
                    } catch (error: Exception) {
                        repository.reportError(error.message)
                    }
                }
            })

            if (prompt != null && !promptVisible) {
                Row(
                    modifier = Modifier
                        .fillMaxWidth()
                        .padding(horizontal = 16.dp, vertical = 6.dp),
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.SpaceBetween,
                ) {
                    Text(
                        text = stringResource(R.string.authentication_required),
                        style = MaterialTheme.typography.bodySmall,
                        color = Muted,
                    )
                    TextButton(onClick = { promptVisible = true }) {
                        Text(stringResource(R.string.answer))
                    }
                }
            }

            Box(
                modifier = Modifier
                    .weight(1f)
                    .fillMaxWidth()
                    .background(MaterialTheme.colorScheme.background),
            ) {
                AndroidView(
                    modifier = Modifier.fillMaxSize(),
                    factory = { viewContext ->
                        TerminalView(viewContext).also { view ->
                            view.bind(repository, sessionID)
                            view.onModesChanged = { appCursor, _ ->
                                appCursorKeys = appCursor
                            }
                            terminalView = view
                        }
                    },
                    update = { view -> terminalView = view },
                )
            }

            KeyBar(
                appCursorKeys = appCursorKeys,
                onBytes = { terminalView?.send(it) },
                onCopy = { terminalView?.copyScreen() },
                onPaste = { terminalView?.pasteClipboard() },
            )
        }
    }

    if (prompt != null && promptVisible) {
        PromptSheet(
            prompt = prompt,
            onAnswer = { request ->
                scope.launch {
                    try {
                        repository.answer(sessionID, request)
                    } catch (error: Exception) {
                        repository.reportError(error.message)
                    }
                }
            },
            onDismiss = { promptVisible = false },
        )
    }

    if (closeConfirm) {
        ConfirmDialog(
            title = stringResource(R.string.close_session_confirm),
            text = title,
            confirmLabel = stringResource(R.string.close),
            destructive = true,
            onConfirm = {
                closeConfirm = false
                scope.launch {
                    try {
                        repository.closeSession(sessionID)
                    } catch (error: Exception) {
                        repository.reportError(error.message)
                    }
                    onBack()
                }
            },
            onDismiss = { closeConfirm = false },
        )
    }
}

@Composable
private fun SessionStatus(session: SessionInfo, onReconnect: () -> Unit) {
    when (session.phase) {
        "failed", "closed" -> {
            Surface(
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(horizontal = 12.dp, vertical = 6.dp),
                shape = RoundedCornerShape(12.dp),
                color = Elevated,
            ) {
                Row(
                    modifier = Modifier.padding(horizontal = 14.dp, vertical = 10.dp),
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(10.dp),
                ) {
                    Column(Modifier.weight(1f)) {
                        PhaseLabel(session.phase)
                        if (session.error.isNotEmpty()) {
                            Text(
                                text = session.error,
                                style = MaterialTheme.typography.bodySmall.copy(fontFamily = FontFamily.Monospace),
                                color = ErrorRed,
                                maxLines = 3,
                                overflow = TextOverflow.Ellipsis,
                            )
                        }
                    }
                    TextButton(onClick = onReconnect) {
                        Text(stringResource(R.string.reconnect))
                    }
                }
            }
        }

        "connecting", "authenticating", "hostkey", "password", "passphrase" -> {
            Row(
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(horizontal = 20.dp, vertical = 4.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                PhaseLabel(session.phase)
            }
        }
    }
}
