package dev.ironpark.mako.net

import android.content.Context
import android.content.pm.PackageInfo
import android.content.pm.PackageManager
import android.os.Build
import dev.ironpark.mako.BuildConfig
import dev.ironpark.mako.R
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.withContext
import org.json.JSONArray
import java.io.ByteArrayOutputStream
import java.io.File
import java.io.IOException
import java.net.HttpURLConnection
import java.net.URL
import java.security.MessageDigest

internal data class ReleaseVersion(val major: Int, val minor: Int, val patch: Int) : Comparable<ReleaseVersion> {
    override fun compareTo(other: ReleaseVersion): Int = compareValuesBy(this, other, { it.major }, { it.minor }, { it.patch })
    companion object {
        fun parse(value: String): ReleaseVersion? {
            val match = Regex("^(?:android-v|v)?(\\d+)\\.(\\d+)\\.(\\d+)(?:\\+[^ ]+)?$").matchEntire(value) ?: return null
            val parts = match.groupValues.drop(1).take(3).map { it.toIntOrNull() ?: return null }
            return ReleaseVersion(parts[0], parts[1], parts[2])
        }
    }
}

internal data class AndroidRelease(
    val version: String, val page: String, val notes: String,
    val apkName: String, val apkUrl: String, val size: Long, val digest: String, val sumsUrl: String,
) {
    fun newerThan(installed: String): Boolean {
        val current = ReleaseVersion.parse(installed) ?: return false
        return (ReleaseVersion.parse(version) ?: return false) > current
    }
}

/** Ignore desktop tags, drafts, prereleases and releases without a complete Android asset. */
internal fun latestAndroidRelease(json: String): AndroidRelease? {
    val array = JSONArray(json)
    val found = mutableListOf<AndroidRelease>()
    for (index in 0 until array.length()) {
        val release = array.getJSONObject(index)
        val tag = release.optString("tag_name")
        if (!tag.startsWith("android-v") || ReleaseVersion.parse(tag) == null || release.optBoolean("draft") || release.optBoolean("prerelease")) continue
        val assets = release.optJSONArray("assets") ?: continue
        var apk: org.json.JSONObject? = null
        var sums = ""
        for (assetIndex in 0 until assets.length()) {
            val asset = assets.getJSONObject(assetIndex)
            when (asset.optString("name")) {
                "mako-$tag.apk" -> if (asset.optString("state", "uploaded") == "uploaded") apk = asset
                "SHA256SUMS" -> sums = asset.optString("browser_download_url")
            }
        }
        val binary = apk ?: continue
        val url = binary.optString("browser_download_url")
        val page = release.optString("html_url")
        val digest = binary.optString("digest").removePrefix("sha256:").takeIf { it.matches(Regex("[0-9a-fA-F]{64}")) }.orEmpty()
        if (!releaseAssetUrl(url) || !page.startsWith("https://github.com/ironpark/mako/releases/tag/") || (digest.isEmpty() && !releaseAssetUrl(sums))) continue
        found += AndroidRelease(tag.removePrefix("android-v"), page, release.optString("body"), binary.optString("name"), url,
            binary.optLong("size"), digest, sums.takeIf(::releaseAssetUrl).orEmpty())
    }
    return found.maxByOrNull { ReleaseVersion.parse(it.version)!! }
}

private fun releaseAssetUrl(url: String): Boolean = url.startsWith("https://github.com/ironpark/mako/releases/download/")

internal fun checksumFor(name: String, sums: String): String? = sums.lineSequence().map { it.trim().split(Regex("\\s+"), limit = 2) }
    .firstOrNull { it.size == 2 && it[1].removePrefix("*") == name && it[0].matches(Regex("[0-9a-fA-F]{64}")) }?.first()?.lowercase()

internal fun sha256(data: ByteArray): String = MessageDigest.getInstance("SHA-256").digest(data).hex()
private fun ByteArray.hex(): String = joinToString("") { "%02x".format(it) }

internal interface AppUpdates {
    suspend fun latest(): AndroidRelease?
    suspend fun download(release: AndroidRelease, progress: (Float) -> Unit): File
}

internal class GitHubAppUpdates(private val context: Context) : AppUpdates {
    override suspend fun latest(): AndroidRelease? = withContext(Dispatchers.IO) {
        latestAndroidRelease(fetch("https://api.github.com/repos/ironpark/mako/releases?per_page=30", 2 * 1024 * 1024).toString(Charsets.UTF_8))
    }

    override suspend fun download(release: AndroidRelease, progress: (Float) -> Unit): File = withContext(Dispatchers.IO) {
        val expected = if (release.sumsUrl.isNotEmpty()) checksumFor(release.apkName, fetch(release.sumsUrl, 64 * 1024).toString(Charsets.UTF_8))
            ?: throw IOException(context.getString(R.string.update_checksum_missing)) else release.digest.lowercase()
        if (expected.isEmpty() || release.digest.isNotEmpty() && !release.digest.equals(expected, ignoreCase = true))
            throw IOException(context.getString(R.string.update_checksum_failed))
        val directory = File(context.cacheDir, "updates").apply { mkdirs() }
        val temporary = File(directory, "update-${java.util.UUID.randomUUID()}.part")
        val target = File(directory, "mako-${release.version}.apk")
        val connection = connection(release.apkUrl)
        try {
            val hash = MessageDigest.getInstance("SHA-256")
            connection.inputStream.use { input -> temporary.outputStream().use { output ->
                val buffer = ByteArray(64 * 1024)
                var downloaded = 0L
                var reportedAt = 0L
                while (true) {
                    currentCoroutineContext().ensureActive()
                    val count = input.read(buffer)
                    if (count < 0) break
                    downloaded += count
                    if (downloaded > 200L * 1024 * 1024) throw IOException(context.getString(R.string.update_too_large))
                    output.write(buffer, 0, count)
                    hash.update(buffer, 0, count)
                    if (System.currentTimeMillis() - reportedAt > 100) {
                        progress(if (release.size > 0) (downloaded.toFloat() / release.size).coerceIn(0f, 1f) else 0f)
                        reportedAt = System.currentTimeMillis()
                    }
                }
                if (release.size > 0 && downloaded != release.size) throw IOException(context.getString(R.string.update_checksum_failed))
            } }
            if (hash.digest().hex() != expected) throw IOException(context.getString(R.string.update_checksum_failed))
            verifyPackage(temporary, release)
            if (target.exists() && !target.delete() || !temporary.renameTo(target)) throw IOException(context.getString(R.string.update_download_failed))
            directory.listFiles()?.filter { it != target && it.extension == "apk" }?.forEach { it.delete() }
            progress(1f)
            target
        } finally {
            connection.disconnect()
            temporary.delete()
        }
    }

    @Suppress("DEPRECATION")
    private fun verifyPackage(file: File, release: AndroidRelease) {
        val manager = context.packageManager
        val flags = if (Build.VERSION.SDK_INT >= 28) PackageManager.GET_SIGNING_CERTIFICATES else PackageManager.GET_SIGNATURES
        val archive = manager.getPackageArchiveInfo(file.path, flags) ?: throw IOException(context.getString(R.string.update_invalid_apk))
        val installed = manager.getPackageInfo(context.packageName, flags)
        if (archive.packageName != context.packageName || archive.versionName != release.version) throw IOException(context.getString(R.string.update_invalid_apk))
        val apkCode = if (Build.VERSION.SDK_INT >= 28) archive.longVersionCode else archive.versionCode.toLong()
        val currentCode = if (Build.VERSION.SDK_INT >= 28) installed.longVersionCode else installed.versionCode.toLong()
        if (apkCode <= currentCode) throw IOException(context.getString(R.string.update_downgrade))
        fun certificates(info: PackageInfo): Set<String> =
            (if (Build.VERSION.SDK_INT >= 28) info.signingInfo?.apkContentsSigners else info.signatures).orEmpty().map { sha256(it.toByteArray()) }.toSet()
        val expected = certificates(installed)
        if (expected.isEmpty() || certificates(archive) != expected) throw IOException(context.getString(R.string.update_signature_mismatch))
    }

    private fun fetch(url: String, limit: Int): ByteArray {
        val connection = connection(url)
        try {
            return connection.inputStream.use { input ->
                val output = ByteArrayOutputStream()
                val buffer = ByteArray(8192)
                while (true) {
                    val count = input.read(buffer)
                    if (count < 0) break
                    if (output.size() + count > limit) throw IOException(context.getString(R.string.update_too_large))
                    output.write(buffer, 0, count)
                }
                output.toByteArray()
            }
        } finally { connection.disconnect() }
    }

    private fun connection(url: String): HttpURLConnection = (URL(url).openConnection() as HttpURLConnection).apply {
        connectTimeout = 15_000
        readTimeout = 15_000
        setRequestProperty("Accept", if (url.startsWith("https://api.github.com/")) "application/vnd.github+json" else "application/octet-stream")
        setRequestProperty("User-Agent", "mako-android/${BuildConfig.VERSION_NAME}")
        if (responseCode != HttpURLConnection.HTTP_OK) {
            val code = responseCode
            disconnect()
            throw IOException(context.getString(R.string.update_http_error, code))
        }
    }
}
