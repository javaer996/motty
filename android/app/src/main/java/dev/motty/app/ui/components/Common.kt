package dev.motty.app.ui.components

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import dev.motty.app.R
import dev.motty.app.ui.theme.ErrorRed
import dev.motty.app.ui.theme.Mint
import dev.motty.app.ui.theme.MintLight
import dev.motty.app.ui.theme.Muted

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun MottyTopBar(
    title: String,
    onBack: (() -> Unit)? = null,
    actions: @Composable () -> Unit = {},
) {
    TopAppBar(
        title = {
            Text(
                text = title,
                style = MaterialTheme.typography.titleLarge,
                fontWeight = FontWeight.SemiBold,
            )
        },
        navigationIcon = {
            if (onBack != null) {
                IconButton(onClick = onBack) {
                    Icon(
                        Icons.AutoMirrored.Filled.ArrowBack,
                        contentDescription = stringResource(R.string.cancel),
                    )
                }
            }
        },
        actions = { actions() },
        colors = TopAppBarDefaults.topAppBarColors(
            containerColor = MaterialTheme.colorScheme.background,
            titleContentColor = MaterialTheme.colorScheme.onBackground,
            navigationIconContentColor = MaterialTheme.colorScheme.onBackground,
            actionIconContentColor = MaterialTheme.colorScheme.onBackground,
        ),
    )
}

@Composable
fun SectionHeader(text: String, modifier: Modifier = Modifier) {
    Text(
        text = text,
        modifier = modifier.padding(horizontal = 20.dp, vertical = 10.dp),
        style = MaterialTheme.typography.labelLarge,
        color = Muted,
    )
}

/** A colored dot plus the localized phase name. */
@Composable
fun PhaseLabel(phase: String, modifier: Modifier = Modifier) {
    val color = phaseColor(phase)
    Row(
        modifier = modifier,
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(6.dp),
    ) {
        Box(
            modifier = Modifier
                .size(7.dp)
                .background(color, CircleShape),
        )
        Text(
            text = phaseText(phase),
            style = MaterialTheme.typography.labelMedium,
            color = color,
        )
    }
}

@Composable
fun phaseText(phase: String): String = when (phase) {
    "connecting" -> stringResource(R.string.phase_connecting)
    "authenticating" -> stringResource(R.string.phase_authenticating)
    "hostkey" -> stringResource(R.string.phase_hostkey)
    "password" -> stringResource(R.string.phase_password)
    "passphrase" -> stringResource(R.string.phase_passphrase)
    "connected" -> stringResource(R.string.phase_connected)
    "failed" -> stringResource(R.string.phase_failed)
    "closed" -> stringResource(R.string.phase_closed)
    else -> phase
}

fun phaseColor(phase: String): Color = when (phase) {
    "connected" -> Mint
    "failed" -> ErrorRed
    "closed" -> Muted
    "connecting", "authenticating" -> Muted
    else -> MintLight
}

@Composable
fun ConfirmDialog(
    title: String,
    text: String,
    confirmLabel: String,
    destructive: Boolean = false,
    onConfirm: () -> Unit,
    onDismiss: () -> Unit,
) {
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(title) },
        text = { Text(text) },
        confirmButton = {
            TextButton(onClick = onConfirm) {
                Text(
                    text = confirmLabel,
                    color = if (destructive) ErrorRed else MaterialTheme.colorScheme.primary,
                )
            }
        },
        dismissButton = {
            TextButton(onClick = onDismiss) {
                Text(stringResource(R.string.cancel))
            }
        },
    )
}

@Composable
fun EmptyHint(text: String, modifier: Modifier = Modifier) {
    Box(
        modifier = modifier
            .fillMaxWidth()
            .padding(horizontal = 20.dp, vertical = 28.dp),
        contentAlignment = Alignment.Center,
    ) {
        Text(
            text = text,
            style = MaterialTheme.typography.bodyMedium,
            color = Muted,
        )
    }
}

/** A label/value pair used by the settings and details lists. */
@Composable
fun DetailRow(label: String, value: String, modifier: Modifier = Modifier) {
    Row(
        modifier = modifier
            .fillMaxWidth()
            .padding(horizontal = 20.dp, vertical = 8.dp),
        verticalAlignment = Alignment.Top,
    ) {
        Text(
            text = label,
            modifier = Modifier.width(110.dp),
            style = MaterialTheme.typography.bodyMedium,
            color = Muted,
        )
        Spacer(Modifier.width(8.dp))
        Text(
            text = value,
            style = MaterialTheme.typography.bodyMedium.copy(fontFamily = FontFamily.Monospace),
            color = MaterialTheme.colorScheme.onBackground,
        )
    }
}
