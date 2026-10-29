package dev.ironpark.mako.ui

import android.Manifest
import android.content.pm.PackageManager
import android.os.SystemClock
import android.widget.Toast
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.camera.core.CameraSelector
import androidx.camera.core.ImageProxy
import androidx.camera.view.CameraController
import androidx.camera.view.LifecycleCameraController
import androidx.camera.view.PreviewView
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.core.content.ContextCompat
import androidx.lifecycle.compose.LocalLifecycleOwner
import com.google.zxing.BarcodeFormat
import com.google.zxing.BinaryBitmap
import com.google.zxing.DecodeHintType
import com.google.zxing.PlanarYUVLuminanceSource
import com.google.zxing.ReaderException
import com.google.zxing.common.HybridBinarizer
import com.google.zxing.qrcode.QRCodeReader
import dev.ironpark.mako.Mesh
import dev.ironpark.mako.R
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import java.util.concurrent.Executors

/**
 * ScanScreen reads a computer's pairing code with the camera, the QR code
 * its Settings ▸ Access shows, and joins the computer: it lets this device
 * in at once.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ScanScreen(mesh: Mesh, onBack: () -> Unit) {
    val context = LocalContext.current
    var granted by remember {
        mutableStateOf(ContextCompat.checkSelfPermission(context, Manifest.permission.CAMERA) == PackageManager.PERMISSION_GRANTED)
    }
    val ask = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { granted = it }
    LaunchedEffect(Unit) { if (!granted) ask.launch(Manifest.permission.CAMERA) }

    val join = rememberJoin(mesh)
    var joining by remember { mutableStateOf(false) }
    // A code that failed is not tried again until a moment has passed.
    var failed by remember { mutableStateOf("" to 0L) }

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text(stringResource(R.string.scan_qr)) },
                navigationIcon = {
                    IconButton(onClick = onBack) { Icon(Icons.AutoMirrored.Filled.ArrowBack, stringResource(R.string.back)) }
                },
            )
        },
    ) { pad ->
        Box(Modifier.fillMaxSize().padding(pad)) {
            if (granted) {
                CameraScan(onCode = { link ->
                    val again = link != failed.first || SystemClock.elapsedRealtime() - failed.second > RETRY
                    if (!joining && again && Mesh.isPairing(link)) {
                        joining = true
                        join(link) { ok ->
                            joining = false
                            if (ok) onBack() else failed = link to SystemClock.elapsedRealtime()
                        }
                    }
                })
                Box(
                    Modifier.align(Alignment.Center).size(240.dp)
                        .border(2.dp, Color.White.copy(alpha = 0.8f), RoundedCornerShape(16.dp)),
                )
                Column(
                    Modifier.align(Alignment.BottomCenter).fillMaxWidth()
                        .background(Color.Black.copy(alpha = 0.55f)).padding(20.dp),
                    horizontalAlignment = Alignment.CenterHorizontally,
                    verticalArrangement = Arrangement.spacedBy(12.dp),
                ) {
                    if (joining) CircularProgressIndicator(color = Color.White)
                    Text(
                        stringResource(if (joining) R.string.joining else R.string.scan_qr_note),
                        color = Color.White, textAlign = TextAlign.Center, style = MaterialTheme.typography.bodyMedium,
                    )
                }
            } else {
                Column(
                    Modifier.align(Alignment.Center).padding(32.dp),
                    horizontalAlignment = Alignment.CenterHorizontally,
                    verticalArrangement = Arrangement.spacedBy(16.dp),
                ) {
                    Text(stringResource(R.string.camera_needed), textAlign = TextAlign.Center)
                    FilledTonalButton(onClick = { ask.launch(Manifest.permission.CAMERA) }) {
                        Text(stringResource(R.string.allow_camera))
                    }
                }
            }
        }
    }
}

/**
 * rememberJoin joins a computer by its pairing code's link, saying how it
 * went as a toast, then tells done whether it joined.
 */
@Composable
fun rememberJoin(mesh: Mesh): (String, (Boolean) -> Unit) -> Unit {
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    return remember(mesh) {
        { link, done ->
            scope.launch {
                val ok = try {
                    val name = mesh.join(link, context.getString(R.string.ask_note, Mesh.deviceName()))
                    Toast.makeText(context, context.getString(R.string.joined, name), Toast.LENGTH_SHORT).show()
                    true
                } catch (e: CancellationException) {
                    throw e
                } catch (e: Exception) {
                    Toast.makeText(context, e.message, Toast.LENGTH_LONG).show()
                    false
                }
                done(ok)
            }
        }
    }
}

/** CameraScan shows the back camera, and tells onCode of each QR code it reads. */
@Composable
private fun CameraScan(onCode: (String) -> Unit) {
    val context = LocalContext.current
    val owner = LocalLifecycleOwner.current
    val latest by rememberUpdatedState(onCode)
    val controller = remember { LifecycleCameraController(context) }

    DisposableEffect(owner) {
        val worker = Executors.newSingleThreadExecutor()
        val main = ContextCompat.getMainExecutor(context)
        val reader = QRCodeReader()
        controller.setEnabledUseCases(CameraController.IMAGE_ANALYSIS)
        controller.cameraSelector = CameraSelector.DEFAULT_BACK_CAMERA
        controller.setImageAnalysisAnalyzer(worker) { image ->
            val text = image.use { decode(reader, it) }
            if (text != null) main.execute { latest(text) }
        }
        controller.bindToLifecycle(owner)
        onDispose {
            controller.unbind()
            controller.clearImageAnalysisAnalyzer()
            worker.shutdown()
        }
    }

    AndroidView(
        factory = { PreviewView(it).apply { this.controller = controller } },
        modifier = Modifier.fillMaxSize(),
    )
}

private val hints = mapOf(
    DecodeHintType.POSSIBLE_FORMATS to listOf(BarcodeFormat.QR_CODE),
    DecodeHintType.TRY_HARDER to true,
)

/** decode reads a QR code from a frame's luminance, its first plane; null for none. */
private fun decode(reader: QRCodeReader, image: ImageProxy): String? {
    val plane = image.planes[0]
    val buffer = plane.buffer
    val data = ByteArray(buffer.remaining()).also { buffer.get(it) }
    val source = PlanarYUVLuminanceSource(
        data, plane.rowStride, image.height, 0, 0, image.width, image.height, false,
    )
    return try {
        reader.decode(BinaryBitmap(HybridBinarizer(source)), hints).text
    } catch (_: ReaderException) {
        null
    } finally {
        reader.reset()
    }
}

/** How long a code that failed waits before it is tried again, in ms. */
private const val RETRY = 4_000L
