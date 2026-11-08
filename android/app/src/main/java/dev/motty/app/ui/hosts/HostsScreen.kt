package dev.motty.app.ui.hosts

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
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExtendedFloatingActionButton
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
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import dev.motty.app.R
import dev.motty.app.core.CoreRepository
import dev.motty.app.core.Host
import dev.motty.app.core.SessionInfo
import dev.motty.app.ui.components.ConfirmDialog
import dev.motty.app.ui.components.EmptyHint
import dev.motty.app.ui.components.MottyTopBar
import dev.motty.app.ui.components.PhaseLabel
import dev.motty.app.ui.components.SectionHeader
import dev.motty.app.ui.theme.Elevated
import dev.motty.app.ui.theme.Mint
import dev.motty.app.ui.theme.Muted
import kotlinx.coroutines.launch

@Composable
fun HostsScreen(
    repository: CoreRepository,
    onOpenSession: (Long) -> Unit,
    onAddHost: () -> Unit,
    onEditHost: (String) -> Unit,
    onKeys: () -> Unit,
    onSettings: () -> Unit,
) {
    val hosts by repository.hosts.collectAsState()
    val sessions by repository.sessions.collectAsState()
    val info by repository.info.collectAsState()
    val scope = rememberCoroutineScope()
    var busy by remember { mutableStateOf(false) }
    var deleteHost by remember { mutableStateOf<Host?>(null) }
    var closeSession by remember { mutableStateOf<SessionInfo?>(null) }

    fun open(openSession: suspend () -> SessionInfo) {
        if (busy) return
        busy = true
        scope.launch {
            try {
                onOpenSession(openSession().id)
            } catch (error: Exception) {
                repository.reportError(error.message)
            } finally {
                busy = false
            }
        }
    }

    Scaffold(
        containerColor = MaterialTheme.colorScheme.background,
        topBar = {
            MottyTopBar(
                title = stringResource(R.string.app_name),
                actions = {
                    TextButton(onClick = onKeys) {
                        Text(stringResource(R.string.keys))
                    }
                    IconButton(onClick = onSettings) {
                        Icon(Icons.Default.Settings, contentDescription = stringResource(R.string.settings))
                    }
                },
            )
        },
        floatingActionButton = {
            ExtendedFloatingActionButton(
                onClick = onAddHost,
                containerColor = Mint,
                contentColor = MaterialTheme.colorScheme.background,
                icon = { Icon(Icons.Default.Add, contentDescription = null) },
                text = { Text(stringResource(R.string.add_host)) },
            )
        },
    ) { padding ->
        LazyColumn(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding),
            contentPadding = PaddingValues(bottom = 96.dp),
        ) {
            if (info?.localShell == true) {
                item(key = "local-header") {
                    SectionHeader(stringResource(R.string.local_shell))
                }
                item(key = "local-row") {
                    LocalShellRow(enabled = !busy) {
                        open { repository.openLocalSession() }
                    }
                }
            }

            if (sessions.isNotEmpty()) {
                item(key = "sessions-header") {
                    SectionHeader(stringResource(R.string.active_sessions))
                }
                items(sessions.values.sortedBy { it.id }, key = { "s${it.id}" }) { session ->
                    SessionRow(
                        session = session,
                        onClick = { onOpenSession(session.id) },
                        onClose = { closeSession = session },
                    )
                }
            }

            val grouped = hosts.groupBy { it.group }
            grouped.forEach { (group, entries) ->
                item(key = "g:$group") {
                    SectionHeader(
                        group.ifEmpty { stringResource(R.string.hosts_section) },
                    )
                }
                items(entries, key = { "h:${it.id}" }) { host ->
                    HostRow(
                        host = host,
                        enabled = !busy,
                        onClick = { open { repository.openSession(host.id) } },
                        onEdit = { onEditHost(host.id) },
                        onDelete = { deleteHost = host },
                    )
                }
            }

            if (hosts.isEmpty() && info?.localShell != true) {
                item(key = "empty") {
                    EmptyHint(stringResource(R.string.no_hosts))
                }
            }
        }
    }

    deleteHost?.let { host ->
        ConfirmDialog(
            title = stringResource(R.string.delete_host_confirm, host.label),
            text = stringResource(R.string.delete_host_message),
            confirmLabel = stringResource(R.string.delete),
            destructive = true,
            onConfirm = {
                deleteHost = null
                scope.launch {
                    try {
                        repository.deleteHost(host.id)
                    } catch (error: Exception) {
                        repository.reportError(error.message)
                    }
                }
            },
            onDismiss = { deleteHost = null },
        )
    }

    closeSession?.let { session ->
        ConfirmDialog(
            title = stringResource(R.string.close_session_confirm),
            text = session.title.ifEmpty { session.address },
            confirmLabel = stringResource(R.string.close),
            destructive = true,
            onConfirm = {
                closeSession = null
                scope.launch {
                    try {
                        repository.closeSession(session.id)
                    } catch (error: Exception) {
                        repository.reportError(error.message)
                    }
                }
            },
            onDismiss = { closeSession = null },
        )
    }
}

@Composable
private fun LocalShellRow(enabled: Boolean, onClick: () -> Unit) {
    Surface(
        onClick = onClick,
        enabled = enabled,
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 16.dp, vertical = 4.dp),
        shape = RoundedCornerShape(14.dp),
        color = Elevated,
    ) {
        Row(
            modifier = Modifier.padding(horizontal = 16.dp, vertical = 14.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Box(
                modifier = Modifier
                    .size(34.dp)
                    .background(Mint.copy(alpha = 0.18f), CircleShape),
                contentAlignment = Alignment.Center,
            ) {
                Text(">_", color = Mint, fontFamily = FontFamily.Monospace)
            }
            Spacer(Modifier.width(14.dp))
            Column(Modifier.weight(1f)) {
                Text(
                    text = stringResource(R.string.local_shell),
                    style = MaterialTheme.typography.bodyLarge,
                    fontWeight = FontWeight.Medium,
                    color = MaterialTheme.colorScheme.onSurface,
                )
                Text(
                    text = "/system/bin/sh",
                    style = MaterialTheme.typography.bodySmall.copy(fontFamily = FontFamily.Monospace),
                    color = Muted,
                )
            }
        }
    }
}

@Composable
private fun SessionRow(session: SessionInfo, onClick: () -> Unit, onClose: () -> Unit) {
    Surface(
        onClick = onClick,
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 16.dp, vertical = 4.dp),
        shape = RoundedCornerShape(14.dp),
        color = Elevated,
    ) {
        Row(
            modifier = Modifier.padding(start = 16.dp, top = 10.dp, bottom = 10.dp, end = 4.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Column(Modifier.weight(1f)) {
                Text(
                    text = session.title.ifEmpty { session.address },
                    style = MaterialTheme.typography.bodyLarge,
                    color = MaterialTheme.colorScheme.onSurface,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
                Row(
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(8.dp),
                ) {
                    PhaseLabel(session.phase)
                    if (session.local) {
                        Text(
                            text = stringResource(R.string.local_shell),
                            style = MaterialTheme.typography.labelSmall,
                            color = Muted,
                        )
                    } else if (session.user.isNotEmpty()) {
                        Text(
                            text = "${session.user}@${session.address}",
                            style = MaterialTheme.typography.labelSmall.copy(fontFamily = FontFamily.Monospace),
                            color = Muted,
                            maxLines = 1,
                            overflow = TextOverflow.Ellipsis,
                        )
                    }
                }
            }
            IconButton(onClick = onClose) {
                Icon(
                    Icons.Default.Close,
                    contentDescription = stringResource(R.string.close),
                    tint = Muted,
                )
            }
        }
    }
}

@Composable
private fun HostRow(
    host: Host,
    enabled: Boolean,
    onClick: () -> Unit,
    onEdit: () -> Unit,
    onDelete: () -> Unit,
) {
    var menuOpen by remember { mutableStateOf(false) }
    Surface(
        onClick = onClick,
        enabled = enabled,
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 16.dp, vertical = 4.dp),
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
                Text(
                    text = host.label.take(1).uppercase(),
                    color = Mint,
                    style = MaterialTheme.typography.titleMedium,
                )
            }
            Spacer(Modifier.width(14.dp))
            Column(Modifier.weight(1f)) {
                Text(
                    text = host.label,
                    style = MaterialTheme.typography.bodyLarge,
                    color = MaterialTheme.colorScheme.onSurface,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
                Text(
                    text = buildString {
                        if (host.user.isNotEmpty()) append(host.user).append("@")
                        append(host.address)
                        if (host.port != 0 && host.port != 22) append(":").append(host.port)
                    },
                    style = MaterialTheme.typography.bodySmall.copy(fontFamily = FontFamily.Monospace),
                    color = Muted,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
            }
            Box {
                IconButton(onClick = { menuOpen = true }) {
                    Icon(Icons.Default.MoreVert, contentDescription = null, tint = Muted)
                }
                DropdownMenu(expanded = menuOpen, onDismissRequest = { menuOpen = false }) {
                    DropdownMenuItem(
                        text = { Text(stringResource(R.string.edit_host)) },
                        onClick = {
                            menuOpen = false
                            onEdit()
                        },
                    )
                    DropdownMenuItem(
                        text = {
                            Text(stringResource(R.string.delete), color = MaterialTheme.colorScheme.error)
                        },
                        onClick = {
                            menuOpen = false
                            onDelete()
                        },
                    )
                }
            }
        }
    }
}
