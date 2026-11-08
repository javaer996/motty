package dev.motty.app.ui.terminal

import androidx.compose.foundation.background
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import dev.motty.app.ui.theme.Elevated
import dev.motty.app.ui.theme.Muted

/**
 * The row of terminal keys above the keyboard: control characters, arrows and
 * copy/paste. It scrolls horizontally so narrow phones can still reach every
 * key.
 */
@Composable
fun KeyBar(
    appCursorKeys: Boolean,
    onBytes: (ByteArray) -> Unit,
    onCopy: () -> Unit,
    onPaste: () -> Unit,
    modifier: Modifier = Modifier,
) {
    val keys = listOf(
        KeyCap("esc", byteArrayOf(0x1B)),
        KeyCap("tab", byteArrayOf(0x09)),
        KeyCap("^C", byteArrayOf(0x03)),
        KeyCap("^D", byteArrayOf(0x04)),
        KeyCap("^Z", byteArrayOf(0x1A)),
        KeyCap("^L", byteArrayOf(0x0C)),
        KeyCap("←", KeyEncoder.cursor('D', appCursorKeys)),
        KeyCap("↑", KeyEncoder.cursor('A', appCursorKeys)),
        KeyCap("↓", KeyEncoder.cursor('B', appCursorKeys)),
        KeyCap("→", KeyEncoder.cursor('C', appCursorKeys)),
        KeyCap("home", if (appCursorKeys) KeyEncoder.sequence("OH") else KeyEncoder.sequence("[H")),
        KeyCap("end", if (appCursorKeys) KeyEncoder.sequence("OF") else KeyEncoder.sequence("[F")),
        KeyCap("pgup", KeyEncoder.sequence("[5~")),
        KeyCap("pgdn", KeyEncoder.sequence("[6~")),
    )
    Row(
        modifier = modifier
            .fillMaxWidth()
            .background(MaterialTheme.colorScheme.background)
            .horizontalScroll(rememberScrollState())
            .padding(horizontal = 8.dp, vertical = 6.dp),
        horizontalArrangement = Arrangement.spacedBy(6.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        keys.forEach { key ->
            KeyButton(label = key.label) { onBytes(key.bytes) }
        }
        KeyButton(label = "copy", onClick = onCopy)
        KeyButton(label = "paste", onClick = onPaste)
    }
}

private data class KeyCap(val label: String, val bytes: ByteArray)

@Composable
private fun KeyButton(label: String, onClick: () -> Unit) {
    Surface(
        onClick = onClick,
        shape = RoundedCornerShape(8.dp),
        color = Elevated,
        modifier = Modifier.height(36.dp),
    ) {
        Row(
            modifier = Modifier.padding(horizontal = 12.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text(
                text = label,
                color = Muted,
                fontFamily = FontFamily.Monospace,
                fontSize = 12.sp,
            )
        }
    }
}
