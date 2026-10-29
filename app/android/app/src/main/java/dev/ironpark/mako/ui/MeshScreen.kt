package dev.ironpark.mako.ui

import android.content.ClipData
import android.content.Intent
import android.widget.Toast
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.outlined.Add
import androidx.compose.material.icons.outlined.Close
import androidx.compose.material.icons.outlined.ContentCopy
import androidx.compose.material.icons.outlined.Edit
import androidx.compose.material.icons.outlined.QrCodeScanner
import androidx.compose.material.icons.outlined.Share
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.ListItem
import androidx.compose.material3.ListItemDefaults
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.ClipEntry
import androidx.compose.ui.platform.LocalClipboard
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import dev.ironpark.mako.Mesh
import dev.ironpark.mako.R
import dev.ironpark.mako.v2.Node
import kotlinx.coroutines.launch

/**
 * MeshScreen is this device's place on the mesh: its name and key, the
 * computers it knows and whether each lets it in, asking those that do not,
 * and the addresses of computers mDNS does not find.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun MeshScreen(mesh: Mesh, onBack: () -> Unit, onScan: () -> Unit) {
    val self by mesh.self.collectAsState()
    val nodes by mesh.nodes.collectAsState()
    val access by mesh.access.collectAsState()
    val peers by mesh.peers.collectAsState()
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    val clipboard = LocalClipboard.current
    var adding by remember { mutableStateOf(false) }
    var naming by remember { mutableStateOf(false) }
    var asked by remember { mutableStateOf(setOf<String>()) }

    val act = actor()

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text(stringResource(R.string.computers)) },
                navigationIcon = {
                    IconButton(onClick = onBack) { Icon(Icons.AutoMirrored.Filled.ArrowBack, stringResource(R.string.back)) }
                },
            )
        },
    ) { pad ->
        Column(Modifier.fillMaxSize().padding(pad).verticalScroll(rememberScrollState())) {
            Section(stringResource(R.string.this_device)) {
                Column {
                    ListItem(
                        headlineContent = { Text(self?.name.orEmpty()) },
                        supportingContent = { Text(stringResource(R.string.device_name_note)) },
                        trailingContent = {
                            IconButton(onClick = { naming = true }) { Icon(Icons.Outlined.Edit, stringResource(R.string.rename)) }
                        },
                        colors = ListItemDefaults.colors(containerColor = Color.Transparent),
                    )
                    val key = access?.key
                    if (key != null && key.line.isNotEmpty()) {
                        HorizontalDivider()
                        ListItem(
                            headlineContent = { Text(stringResource(R.string.device_key)) },
                            supportingContent = { Text(key.fingerprint, fontFamily = FontFamily.Monospace, style = MaterialTheme.typography.bodySmall) },
                            trailingContent = {
                                Row {
                                    IconButton(onClick = {
                                        scope.launch {
                                            clipboard.setClipEntry(ClipEntry(ClipData.newPlainText("mako", key.line)))
                                            Toast.makeText(context, R.string.key_copied, Toast.LENGTH_SHORT).show()
                                        }
                                    }) { Icon(Icons.Outlined.ContentCopy, stringResource(R.string.copy)) }
                                    IconButton(onClick = {
                                        val send = Intent(Intent.ACTION_SEND).setType("text/plain").putExtra(Intent.EXTRA_TEXT, key.line)
                                        context.startActivity(Intent.createChooser(send, null))
                                    }) { Icon(Icons.Outlined.Share, stringResource(R.string.share)) }
                                }
                            },
                            colors = ListItemDefaults.colors(containerColor = Color.Transparent),
                        )
                    }
                }
            }

            Section(stringResource(R.string.computers)) {
                Column {
                    ListItem(
                        leadingContent = { Icon(Icons.Outlined.QrCodeScanner, null, tint = MaterialTheme.colorScheme.primary) },
                        headlineContent = { Text(stringResource(R.string.scan_qr)) },
                        supportingContent = { Text(stringResource(R.string.scan_qr_hint), style = MaterialTheme.typography.bodySmall) },
                        modifier = Modifier.clickable(onClick = onScan),
                        colors = ListItemDefaults.colors(containerColor = Color.Transparent),
                    )
                    HorizontalDivider()
                    if (nodes.isEmpty()) {
                        Text(
                            stringResource(R.string.no_computers), style = MaterialTheme.typography.bodyMedium,
                            color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(16.dp),
                        )
                    }
                    nodes.forEachIndexed { i, n ->
                        if (i > 0) HorizontalDivider()
                        NodeItem(n, asked = n.id in asked, onAsk = {
                            act {
                                mesh.askToJoin(n.id, context.getString(R.string.ask_note, Mesh.deviceName()))
                                asked = asked + n.id
                            }
                        })
                    }
                }
            }

            Section(stringResource(R.string.addresses)) {
                Column {
                    Text(
                        stringResource(R.string.addresses_note), style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                        modifier = Modifier.padding(start = 16.dp, end = 16.dp, top = 16.dp),
                    )
                    for (p in peers) {
                        ListItem(
                            headlineContent = { Text(p, fontFamily = FontFamily.Monospace) },
                            trailingContent = {
                                IconButton(onClick = { act { mesh.removePeer(p) } }) {
                                    Icon(Icons.Outlined.Close, stringResource(R.string.remove))
                                }
                            },
                            colors = ListItemDefaults.colors(containerColor = Color.Transparent),
                        )
                    }
                    TextButton(onClick = { adding = true }, modifier = Modifier.padding(8.dp)) {
                        Icon(Icons.Outlined.Add, null)
                        Spacer(Modifier.width(8.dp))
                        Text(stringResource(R.string.add_address))
                    }
                }
            }
        }
    }

    if (adding) {
        TextDialog(
            title = stringResource(R.string.add_address),
            initial = "",
            hint = stringResource(R.string.address_hint),
            action = stringResource(R.string.add),
            onDismiss = { adding = false },
            onDone = {
                adding = false
                act { mesh.addPeer(it) }
            },
        )
    }
    if (naming) {
        TextDialog(
            title = stringResource(R.string.rename),
            initial = self?.name.orEmpty(),
            onDismiss = { naming = false },
            onDone = {
                naming = false
                act { mesh.rename(it) }
            },
        )
    }
}

@Composable
private fun NodeItem(n: Node, asked: Boolean, onAsk: () -> Unit) {
    ListItem(
        leadingContent = {
            Icon(
                osIcon(n.os), null,
                tint = if (n.trustsUs) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.onSurfaceVariant,
            )
        },
        headlineContent = { Text(n.name, maxLines = 1, overflow = TextOverflow.Ellipsis) },
        supportingContent = {
            Text(
                dotted(osName(n.os), n.addr.ifEmpty { n.source }),
                style = MaterialTheme.typography.bodySmall,
            )
        },
        trailingContent = {
            when {
                n.trustsUs -> Pill(stringResource(R.string.lets_in), StatusColors.Done)
                asked -> Text(
                    stringResource(R.string.asked, n.name), style = MaterialTheme.typography.labelSmall,
                    color = StatusColors.Waiting, modifier = Modifier.width(120.dp),
                )
                else -> FilledTonalButton(onClick = onAsk) { Text(stringResource(R.string.ask_to_join)) }
            }
        },
        colors = ListItemDefaults.colors(containerColor = Color.Transparent),
    )
}
