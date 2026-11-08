package dev.motty.app.ui

import android.net.Uri
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.navigation.NavType
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.rememberNavController
import androidx.navigation.navArgument
import dev.motty.app.R
import dev.motty.app.core.CoreRepository
import dev.motty.app.service.SessionKeepAliveService
import dev.motty.app.ui.hosts.HostEditorScreen
import dev.motty.app.ui.hosts.HostsScreen
import dev.motty.app.ui.keys.KeysScreen
import dev.motty.app.ui.settings.SettingsScreen
import dev.motty.app.ui.terminal.SessionScreen
import dev.motty.app.ui.theme.ErrorRed
import dev.motty.app.ui.theme.MottyTheme
import dev.motty.app.ui.theme.Muted

private object Routes {
    const val HOSTS = "hosts"
    const val HOST_EDITOR = "hostEditor/{hostId}"
    const val KEYS = "keys"
    const val SETTINGS = "settings"
    const val TERMINAL = "terminal/{sessionId}"

    fun hostEditor(hostID: String?): String =
        "hostEditor/" + Uri.encode(hostID ?: "new")

    fun terminal(sessionID: Long): String = "terminal/$sessionID"
}

@Composable
fun MottyApp(repository: CoreRepository) {
    val ready by repository.ready.collectAsState()
    val fatal by repository.fatalError.collectAsState()
    val errorMessage by repository.errorMessage.collectAsState()
    val snackbarHostState = remember { SnackbarHostState() }

    LaunchedEffect(errorMessage) {
        val message = errorMessage ?: return@LaunchedEffect
        snackbarHostState.showSnackbar(message)
        repository.clearError()
    }

    MottyTheme {
        when {
            fatal != null -> FatalScreen(fatal.orEmpty())
            !ready -> LoadingScreen()
            else -> {
                val context = LocalContext.current
                val sessions by repository.sessions.collectAsState()
                val prompts by repository.prompts.collectAsState()
                LaunchedEffect(sessions, prompts) {
                    SessionKeepAliveService.sync(context, sessions, prompts.isNotEmpty())
                }
                val navController = rememberNavController()
                Box(Modifier.fillMaxSize()) {
                    NavHost(
                        navController = navController,
                        startDestination = Routes.HOSTS,
                    ) {
                        composable(Routes.HOSTS) {
                            HostsScreen(
                                repository = repository,
                                onOpenSession = { id -> navController.navigate(Routes.terminal(id)) },
                                onAddHost = { navController.navigate(Routes.hostEditor(null)) },
                                onEditHost = { id -> navController.navigate(Routes.hostEditor(id)) },
                                onKeys = { navController.navigate(Routes.KEYS) },
                                onSettings = { navController.navigate(Routes.SETTINGS) },
                            )
                        }
                        composable(
                            route = Routes.HOST_EDITOR,
                            arguments = listOf(navArgument("hostId") { type = NavType.StringType }),
                        ) { entry ->
                            val hostID = entry.arguments?.getString("hostId")?.takeIf { it != "new" }
                            HostEditorScreen(
                                repository = repository,
                                hostID = hostID,
                                onBack = { navController.popBackStack() },
                            )
                        }
                        composable(Routes.KEYS) {
                            KeysScreen(
                                repository = repository,
                                onBack = { navController.popBackStack() },
                            )
                        }
                        composable(Routes.SETTINGS) {
                            SettingsScreen(
                                repository = repository,
                                onBack = { navController.popBackStack() },
                            )
                        }
                        composable(
                            route = Routes.TERMINAL,
                            arguments = listOf(navArgument("sessionId") { type = NavType.LongType }),
                        ) { entry ->
                            SessionScreen(
                                repository = repository,
                                sessionID = entry.arguments?.getLong("sessionId") ?: 0L,
                                onBack = { navController.popBackStack() },
                            )
                        }
                    }
                    SnackbarHost(
                        hostState = snackbarHostState,
                        modifier = Modifier
                            .align(Alignment.BottomCenter)
                            .navigationBarsPadding(),
                    )
                }
            }
        }
    }
}

@Composable
private fun LoadingScreen() {
    Surface(
        modifier = Modifier.fillMaxSize(),
        color = MaterialTheme.colorScheme.background,
    ) {
        Column(
            modifier = Modifier.fillMaxSize(),
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = androidx.compose.foundation.layout.Arrangement.Center,
        ) {
            Text(
                text = stringResource(R.string.app_name),
                style = MaterialTheme.typography.headlineMedium,
                color = MaterialTheme.colorScheme.primary,
            )
            CircularProgressIndicator(
                modifier = Modifier.padding(top = 20.dp),
                color = MaterialTheme.colorScheme.primary,
            )
        }
    }
}

@Composable
private fun FatalScreen(message: String) {
    Surface(
        modifier = Modifier.fillMaxSize(),
        color = MaterialTheme.colorScheme.background,
    ) {
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(28.dp),
            verticalArrangement = androidx.compose.foundation.layout.Arrangement.Center,
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            Text(
                text = stringResource(R.string.fatal_error_title),
                style = MaterialTheme.typography.titleLarge,
                color = ErrorRed,
                textAlign = TextAlign.Center,
            )
            Text(
                text = message,
                modifier = Modifier.padding(top = 12.dp),
                style = MaterialTheme.typography.bodySmall,
                color = Muted,
                textAlign = TextAlign.Center,
            )
        }
    }
}
