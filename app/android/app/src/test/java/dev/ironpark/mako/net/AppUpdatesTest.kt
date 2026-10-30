package dev.ironpark.mako.net

import org.junit.Assert.*
import org.junit.Test

class AppUpdatesTest {
    @Test fun versionsCompareNumericallyAndRejectPrereleasesAndInvalidTags() {
        assertTrue(ReleaseVersion.parse("android-v0.10.0")!! > ReleaseVersion.parse("0.9.99")!!)
        assertEquals(ReleaseVersion.parse("v1.2.3"), ReleaseVersion.parse("1.2.3+build.7"))
        for (version in listOf("1.2", "1.2.3-rc1", "1.2.999999999999", "../1.2.3", "android-v1.2.3.apk")) assertNull(ReleaseVersion.parse(version))
        val release = AndroidRelease("1.2.3", "", "", "", "", 0, "", "")
        assertTrue(release.newerThan("1.2.2"))
        assertFalse(release.newerThan("1.2.3"))
        assertFalse(release.newerThan("1.3.0"))
    }

    @Test fun checksumsMustMatchTheExactAssetNameAndValidSha256() {
        val digest = sha256("hello".toByteArray())
        assertEquals("2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824", digest)
        assertEquals(digest, checksumFor("mako.apk", "$digest  other.apk\n${digest.uppercase()} *mako.apk\n"))
        assertNull(checksumFor("mako.apk", "$digest  another-mako.apk"))
        assertNull(checksumFor("mako.apk", "bad-checksum  mako.apk"))
    }
}
