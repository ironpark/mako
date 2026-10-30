package dev.ironpark.mako.ui

import android.Manifest
import android.app.NotificationManager
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.provider.Settings
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.toggleable
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.outlined.Notifications
import androidx.compose.material.icons.outlined.SystemUpdate
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.unit.dp
import androidx.core.app.NotificationManagerCompat
import androidx.core.content.ContextCompat
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.compose.LocalLifecycleOwner
import dev.ironpark.mako.BuildConfig
import dev.ironpark.mako.NotificationPreferences
import dev.ironpark.mako.R

@OptIn(ExperimentalMaterial3Api::class)
@Composable
internal fun SettingsScreen(onBack: () -> Unit, onNotifications: () -> Unit, onUpdates: () -> Unit) {
    Scaffold(topBar = {
        TopAppBar(title = { Text(stringResource(R.string.settings)) }, navigationIcon = {
            IconButton(onClick = onBack) { Icon(Icons.AutoMirrored.Filled.ArrowBack, stringResource(R.string.back)) }
        })
    }) { padding ->
        Column(Modifier.fillMaxSize().padding(padding)) {
            FileRow(stringResource(R.string.notification_settings), stringResource(R.string.notification_settings_summary),
                Icons.Outlined.Notifications, onClick = onNotifications)
            FileRow(stringResource(R.string.app_updates), stringResource(R.string.current_version, BuildConfig.VERSION_NAME),
                Icons.Outlined.SystemUpdate, onClick = onUpdates)
        }
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
internal fun NotificationSettingsScreen(preferences: NotificationPreferences, onBack: () -> Unit) {
    val options by preferences.options.collectAsState()
    val context = LocalContext.current
    val lifecycle = LocalLifecycleOwner.current
    fun systemEnabled(): Boolean = NotificationManagerCompat.from(context).areNotificationsEnabled() &&
        context.getSystemService(NotificationManager::class.java).getNotificationChannel("agents")?.importance != NotificationManager.IMPORTANCE_NONE
    var allowed by remember { mutableStateOf(systemEnabled()) }
    val permission = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { allowed = systemEnabled() }
    DisposableEffect(lifecycle) {
        val observer = LifecycleEventObserver { _, event -> if (event == Lifecycle.Event.ON_RESUME) allowed = systemEnabled() }
        lifecycle.lifecycle.addObserver(observer)
        onDispose { lifecycle.lifecycle.removeObserver(observer) }
    }
    fun openSystem() {
        context.startActivity(Intent(Settings.ACTION_APP_NOTIFICATION_SETTINGS).putExtra(Settings.EXTRA_APP_PACKAGE, context.packageName))
    }

    Scaffold(topBar = {
        TopAppBar(title = { Text(stringResource(R.string.notification_settings)) }, navigationIcon = {
            IconButton(onClick = onBack) { Icon(Icons.AutoMirrored.Filled.ArrowBack, stringResource(R.string.back)) }
        })
    }) { padding ->
        Column(Modifier.fillMaxSize().padding(padding).verticalScroll(rememberScrollState())) {
            if (!allowed) Card(Modifier.fillMaxWidth().padding(12.dp)) {
                Column(Modifier.padding(16.dp)) {
                    Text(stringResource(R.string.notifications_blocked), style = MaterialTheme.typography.titleSmall)
                    Text(stringResource(R.string.notifications_blocked_body), Modifier.padding(vertical = 8.dp), style = MaterialTheme.typography.bodySmall)
                    Button(onClick = {
                        if (Build.VERSION.SDK_INT >= 33 && ContextCompat.checkSelfPermission(context, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED)
                            permission.launch(Manifest.permission.POST_NOTIFICATIONS)
                        else openSystem()
                    }) { Text(stringResource(R.string.enable_notifications)) }
                }
            }
            NotificationOption(stringResource(R.string.notifications_enabled), stringResource(R.string.notifications_enabled_body), options.enabled) {
                preferences.update(options.copy(enabled = it))
            }
            HorizontalDivider(Modifier.padding(horizontal = 16.dp))
            NotificationOption(stringResource(R.string.notify_approvals), stringResource(R.string.notify_approvals_body), options.approvals, options.enabled) {
                preferences.update(options.copy(approvals = it))
            }
            NotificationOption(stringResource(R.string.notify_waiting), stringResource(R.string.notify_waiting_body), options.waiting, options.enabled) {
                preferences.update(options.copy(waiting = it))
            }
            NotificationOption(stringResource(R.string.notify_done), stringResource(R.string.notify_done_body), options.done, options.enabled) {
                preferences.update(options.copy(done = it))
            }
            NotificationOption(stringResource(R.string.notify_foreground), stringResource(R.string.notify_foreground_body), options.foreground, options.enabled) {
                preferences.update(options.copy(foreground = it))
            }
            TextButton(onClick = { openSystem() }, modifier = Modifier.padding(horizontal = 8.dp)) {
                Text(stringResource(R.string.android_notification_settings))
            }
            Text(stringResource(R.string.connection_notification_note), Modifier.padding(16.dp),
                style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
    }
}

@Composable
private fun NotificationOption(title: String, description: String, checked: Boolean, enabled: Boolean = true, onChange: (Boolean) -> Unit) {
    Row(Modifier.fillMaxWidth().toggleable(checked, enabled = enabled, role = Role.Switch, onValueChange = onChange).padding(horizontal = 16.dp, vertical = 12.dp),
        verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(16.dp)) {
        Column(Modifier.weight(1f)) {
            Text(title, style = MaterialTheme.typography.bodyLarge, color = MaterialTheme.colorScheme.onSurface.copy(alpha = if (enabled) 1f else 0.5f))
            Text(description, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
        Switch(checked, onCheckedChange = null, enabled = enabled)
    }
}
