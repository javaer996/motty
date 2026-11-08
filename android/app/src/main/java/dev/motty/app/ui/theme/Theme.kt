package dev.motty.app.ui.theme

import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable

private val DarkColors = darkColorScheme(
    primary = Mint,
    onPrimary = Background,
    secondary = MintLight,
    onSecondary = Background,
    background = Background,
    onBackground = OnBackground,
    surface = Surface,
    onSurface = OnBackground,
    surfaceVariant = Elevated,
    onSurfaceVariant = Muted,
    error = ErrorRed,
    onError = Background,
)

@Composable
fun MottyTheme(content: @Composable () -> Unit) {
    MaterialTheme(
        colorScheme = DarkColors,
        typography = MottyTypography,
        content = content,
    )
}
