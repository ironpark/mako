package dev.ironpark.mako.ui

import android.content.Intent
import android.net.Uri
import android.provider.Settings
import android.text.format.Formatter
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import androidx.core.content.FileProvider
import dev.ironpark.mako.BuildConfig
import dev.ironpark.mako.R
import dev.ironpark.mako.net.AppUpdates
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import java.io.File

@OptIn(ExperimentalMaterial3Api::class)
@Composable
internal fun UpdatesScreen(updates: AppUpdates, onBack: () -> Unit) {
    val context = LocalContext.current
    val uri = LocalUriHandler.current
    val scope = rememberCoroutineScope()
    val installFailed = stringResource(R.string.update_install_failed)
    val downloadFailed = stringResource(R.string.update_download_failed)
    var refresh by remember { mutableIntStateOf(0) }
    var downloading by remember { mutableStateOf(false) }
    var progress by remember { mutableFloatStateOf(0f) }
    var apkPath by rememberSaveable { mutableStateOf("") }
    var apkVersion by rememberSaveable { mutableStateOf("") }
    var error by remember { mutableStateOf("") }
    val release = remoteLoad(updates, refresh) { updates.latest() }

    fun install() {
        val file = File(apkPath)
        if (!file.isFile) { apkPath = ""; return }
        runCatching {
            val content = FileProvider.getUriForFile(context, "${context.packageName}.updates", file)
            context.startActivity(Intent(Intent.ACTION_VIEW).setDataAndType(content, "application/vnd.android.package-archive")
                .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION))
        }.onFailure { error = it.message ?: installFailed }
    }
    val allowInstall = rememberLauncherForActivityResult(ActivityResultContracts.StartActivityForResult()) {
        if (context.packageManager.canRequestPackageInstalls()) install()
    }

    Scaffold(topBar = {
        TopAppBar(title = { Text(stringResource(R.string.app_updates)) }, navigationIcon = {
            IconButton(onClick = onBack) { Icon(Icons.AutoMirrored.Filled.ArrowBack, stringResource(R.string.back)) }
        })
    }) { padding ->
        Column(Modifier.fillMaxSize().padding(padding)) {
            Text(stringResource(R.string.current_version, BuildConfig.VERSION_NAME), Modifier.padding(16.dp), style = MaterialTheme.typography.titleMedium)
            RemoteContent(release, retry = { refresh++ }) { latest ->
                Column(Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(horizontal = 16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                    if (latest == null) Text(stringResource(R.string.no_android_release))
                    else {
                        val newer = latest.newerThan(BuildConfig.VERSION_NAME)
                        Text(stringResource(if (newer) R.string.update_available else R.string.app_up_to_date, latest.version), style = MaterialTheme.typography.titleSmall)
                        if (newer) {
                            Text(Formatter.formatShortFileSize(context, latest.size), style = MaterialTheme.typography.bodySmall)
                            if (downloading) {
                                LinearProgressIndicator(progress = { progress }, modifier = Modifier.fillMaxWidth())
                                Text(stringResource(R.string.update_downloading, (progress * 100).toInt()), style = MaterialTheme.typography.bodySmall)
                            }
                            if (error.isNotEmpty()) Text(error, color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodySmall)
                            if (apkPath.isNotEmpty() && apkVersion == latest.version && File(apkPath).isFile) Button(onClick = {
                                error = ""
                                if (context.packageManager.canRequestPackageInstalls()) install()
                                else allowInstall.launch(Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:${context.packageName}")))
                            }) { Text(stringResource(R.string.install_update)) }
                            else Button(enabled = !downloading, onClick = {
                                downloading = true
                                error = ""
                                progress = 0f
                                scope.launch {
                                    try { apkPath = updates.download(latest) { progress = it }.path; apkVersion = latest.version }
                                    catch (e: CancellationException) { throw e }
                                    catch (e: Exception) { error = e.message ?: downloadFailed }
                                    finally { downloading = false }
                                }
                            }) { Text(stringResource(R.string.download_update)) }
                        }
                        TextButton(onClick = { uri.openUri(latest.page) }) { Text(stringResource(R.string.github_release)) }
                        if (latest.notes.isNotBlank()) MarkdownMessage(latest.notes.take(12_000))
                    }
                    TextButton(enabled = !downloading, onClick = { apkPath = ""; error = ""; refresh++ }) { Text(stringResource(R.string.check_updates)) }
                    Spacer(Modifier.height(16.dp))
                }
            }
        }
    }
}
