package dev.ironpark.mako.ui

import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.util.LruCache
import android.widget.Toast
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.Image
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.automirrored.filled.OpenInNew
import androidx.compose.material.icons.outlined.AttachFile
import androidx.compose.material.icons.outlined.Image
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import dev.ironpark.mako.Mesh
import dev.ironpark.mako.R
import dev.ironpark.mako.net.Daemon
import dev.ironpark.mako.net.NodeProjectFiles
import dev.ironpark.mako.v2.SessionInfo
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.net.HttpURLConnection
import java.net.URI

/** Images read from sessions' nodes, small enough for a thumbnail, by node and path. */
private val thumbnails = LruCache<String, ImageBitmap>(64)

/** How long a thumbnail's longer side is decoded at, in pixels. */
private const val THUMB_PIXELS = 480

private sealed interface Thumb {
    data object Loading : Thumb
    data class Ready(val image: ImageBitmap) : Thumb
    data object Missing : Thumb
}

/**
 * RemoteThumbnail is the image at path on node, read from there, which
 * opens when tapped. One that cannot be read shows its name, or nothing
 * when it is only named: text may name a file that is not there.
 */
@Composable
internal fun RemoteThumbnail(daemon: Daemon, node: String, path: String, named: Boolean, onOpen: () -> Unit, modifier: Modifier = Modifier) {
    val key = node + "\u0000" + path
    var thumb by remember(key) { mutableStateOf(thumbnails.get(key)?.let { Thumb.Ready(it) } ?: Thumb.Loading) }
    LaunchedEffect(key) {
        if (thumb !is Thumb.Loading) return@LaunchedEffect
        thumb = try {
            val file = NodeProjectFiles(daemon, node).read(path, image = true)
            val image = if (file.truncated) null else withContext(Dispatchers.Default) { decodeSampled(file.data.toByteArray(), THUMB_PIXELS) }
            image?.asImageBitmap()?.also { thumbnails.put(key, it) }?.let { Thumb.Ready(it) } ?: Thumb.Missing
        } catch (e: CancellationException) {
            throw e
        } catch (_: Exception) {
            Thumb.Missing
        }
    }
    val shape = RoundedCornerShape(8.dp)
    val border = BorderStroke(1.dp, MaterialTheme.colorScheme.outlineVariant)
    when (val t = thumb) {
        Thumb.Loading -> Surface(modifier.size(64.dp), shape = shape, border = border, color = MaterialTheme.colorScheme.surfaceContainerLow) {
            Box(contentAlignment = Alignment.Center) { CircularProgressIndicator(Modifier.size(18.dp), strokeWidth = 2.dp) }
        }
        Thumb.Missing -> if (!named) FileChip(baseName(path), onClick = onOpen, modifier = modifier)
        is Thumb.Ready -> Surface(modifier.clickable(onClick = onOpen), shape = shape, border = border, color = MaterialTheme.colorScheme.surfaceContainerLow) {
            Image(
                t.image, baseName(path),
                Modifier.heightIn(max = 140.dp).widthIn(max = 200.dp).aspectRatio(t.image.width.toFloat() / t.image.height.coerceAtLeast(1)),
                contentScale = ContentScale.Fit,
            )
        }
    }
}

/** FileChip is a file by its name, as an attachment that is not an image shows. */
@Composable
internal fun FileChip(name: String, modifier: Modifier = Modifier, onClick: (() -> Unit)? = null, trailing: @Composable () -> Unit = {}) {
    Surface(
        modifier.let { if (onClick != null) it.clickable(onClick = onClick) else it },
        shape = RoundedCornerShape(8.dp),
        border = BorderStroke(1.dp, MaterialTheme.colorScheme.outlineVariant),
        color = MaterialTheme.colorScheme.surfaceContainerLow,
    ) {
        Row(Modifier.padding(start = 10.dp, end = 4.dp).heightIn(min = 36.dp), verticalAlignment = Alignment.CenterVertically) {
            Icon(if (imageFile(name)) Icons.Outlined.Image else Icons.Outlined.AttachFile, null,
                Modifier.size(16.dp), tint = MaterialTheme.colorScheme.onSurfaceVariant)
            Spacer(Modifier.width(6.dp))
            Text(name, Modifier.widthIn(max = 180.dp), style = MaterialTheme.typography.labelMedium, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Spacer(Modifier.width(6.dp))
            trailing()
        }
    }
}

/**
 * rememberLinkOpener follows a link in the conversation of session: a
 * path, absolute or relative to its project, in onFile; an address on its
 * node's loopback through a tunnel, an image there previewed here; any
 * other address in the system's handler.
 */
@Composable
internal fun rememberLinkOpener(mesh: Mesh, session: SessionInfo, onFile: (ChatLink.File) -> Unit): (String) -> Unit {
    val context = LocalContext.current
    val resources = LocalResources.current
    val uri = LocalUriHandler.current
    val scope = rememberCoroutineScope()
    val current by rememberUpdatedState(session)
    val file by rememberUpdatedState(onFile)
    var preview by remember { mutableStateOf<String?>(null) }
    preview?.let { url -> ImageUrlDialog(url, onBrowser = { preview = null; uri.openUri(url) }, onDismiss = { preview = null }) }
    return remember(context, resources, uri) {
        { dest ->
            val s = current
            when (val link = chatLink(dest, s.project)) {
                null -> {}
                is ChatLink.Web -> uri.openUri(link.url)
                is ChatLink.File -> file(link)
                is ChatLink.Loopback -> if (!mesh.canTransfer(s.nodeId)) uri.openUri(link.url) else scope.launch {
                    val local = try {
                        mesh.tunnels.forward(s.nodeId, link.port)
                    } catch (e: CancellationException) {
                        throw e
                    } catch (e: Exception) {
                        Toast.makeText(context, resources.getString(R.string.tunnel_failed, link.port, mesh.nodeName(s.nodeId)) +
                            e.message?.let { "\n$it" }.orEmpty(), Toast.LENGTH_LONG).show()
                        return@launch
                    }
                    if (local != link.port) Toast.makeText(context, resources.getString(R.string.tunnel_other_port, link.port, local), Toast.LENGTH_SHORT).show()
                    val url = link.at(local)
                    if (link.image) preview = url
                    else runCatching { uri.openUri(url) }.onFailure {
                        Toast.makeText(context, R.string.link_open_failed, Toast.LENGTH_SHORT).show()
                    }
                }
            }
        }
    }
}

/** ImageUrlDialog previews the image a tunnel here serves at url. */
@Composable
private fun ImageUrlDialog(url: String, onBrowser: () -> Unit, onDismiss: () -> Unit) {
    val result = remoteLoad(url) {
        withContext(Dispatchers.IO) { decodeSampled(fetch(url), 2048) ?: error("not an image") }.asImageBitmap()
    }
    Dialog(onDismissRequest = onDismiss, properties = DialogProperties(usePlatformDefaultWidth = false)) {
        Surface(Modifier.fillMaxSize()) {
            Column(Modifier.safeDrawingPadding()) {
                Row(Modifier.fillMaxWidth().padding(8.dp), verticalAlignment = Alignment.CenterVertically) {
                    IconButton(onClick = onDismiss) { Icon(Icons.AutoMirrored.Filled.ArrowBack, stringResource(R.string.back)) }
                    Text(baseName(runCatching { URI(url).path }.getOrNull().orEmpty()).ifEmpty { url }, Modifier.weight(1f),
                        style = MaterialTheme.typography.titleMedium, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    IconButton(onClick = onBrowser) { Icon(Icons.AutoMirrored.Filled.OpenInNew, stringResource(R.string.open_in_browser)) }
                }
                RemoteContent(result, retry = onBrowser) { image ->
                    Image(image, null, Modifier.fillMaxSize().padding(8.dp), contentScale = ContentScale.Fit)
                }
            }
        }
    }
}

/** The body at url, a tunnel on this phone's loopback; at most 32 MB of it. */
private fun fetch(url: String): ByteArray {
    val conn = URI(url).toURL().openConnection() as HttpURLConnection
    conn.connectTimeout = 10_000
    conn.readTimeout = 20_000
    try {
        if (conn.responseCode !in 200..299) error("HTTP ${conn.responseCode}")
        return conn.inputStream.use { input ->
            val out = java.io.ByteArrayOutputStream()
            val buf = ByteArray(64 * 1024)
            while (true) {
                val n = input.read(buf)
                if (n < 0) break
                out.write(buf, 0, n)
                if (out.size() > 32 shl 20) error("too large")
            }
            out.toByteArray()
        }
    } finally {
        conn.disconnect()
    }
}

/** data decoded as an image whose longer side is about at most pixels long. */
internal fun decodeSampled(data: ByteArray, pixels: Int): Bitmap? {
    if (data.isEmpty()) return null
    val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
    BitmapFactory.decodeByteArray(data, 0, data.size, bounds)
    if (bounds.outWidth <= 0 || bounds.outHeight <= 0) return null
    var sample = 1
    while (maxOf(bounds.outWidth, bounds.outHeight) / (sample * 2) >= pixels) sample *= 2
    return BitmapFactory.decodeByteArray(data, 0, data.size, BitmapFactory.Options().apply { inSampleSize = sample })
}
