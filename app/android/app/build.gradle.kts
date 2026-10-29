plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.compose)
}

android {
    namespace = "dev.ironpark.mako"
    compileSdk = 37

    defaultConfig {
        applicationId = "dev.ironpark.mako"
        minSdk = 26
        targetSdk = 36
        versionCode = providers.gradleProperty("makoVersionCode").orElse("1").get().toInt()
        versionName = providers.gradleProperty("makoVersionName").orElse("0.1.0").get()
        // The node is built for these alone (app/Makefile).
        ndk { abiFilters += listOf("arm64-v8a", "x86_64") }
    }

    val releaseKeystore = providers.environmentVariable("ANDROID_KEYSTORE_PATH").orNull
    if (releaseKeystore != null) {
        signingConfigs.create("release") {
            storeFile = file(releaseKeystore)
            storePassword = providers.environmentVariable("ANDROID_KEYSTORE_PASSWORD").get()
            keyAlias = "mako"
            keyPassword = providers.environmentVariable("ANDROID_KEYSTORE_PASSWORD").get()
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = true
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
            // Without a release key, local release builds stay unsigned.
            if (releaseKeystore != null) signingConfig = signingConfigs.getByName("release")
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    buildFeatures {
        compose = true
        buildConfig = true
    }

    // The protocol's messages, generated from proto/ (buf.gen.yaml).
    sourceSets["main"].java.srcDir("src/gen/java")

    packaging { jniLibs { useLegacyPackaging = true } }
}

dependencies {
    testImplementation("junit:junit:4.13.2")
    implementation(files("libs/makocore.aar"))

    implementation(libs.androidx.core.ktx)
    implementation(libs.androidx.activity.compose)
    implementation(libs.androidx.lifecycle.runtime.compose)
    implementation(libs.androidx.lifecycle.process)
    implementation(libs.androidx.navigation.compose)
    implementation(platform(libs.compose.bom))
    implementation(libs.compose.ui)
    implementation(libs.compose.ui.tooling.preview)
    implementation(libs.compose.material3)
    implementation(libs.compose.material.icons)
    debugImplementation(libs.compose.ui.tooling)
    implementation(libs.kotlinx.coroutines.android)
    implementation(libs.protobuf.javalite)
    implementation(libs.termux.emulator)
    implementation(libs.termux.view)
    // Reading a computer's pairing code with the camera.
    implementation(libs.camerax.camera2)
    implementation(libs.camerax.lifecycle)
    implementation(libs.camerax.view)
    implementation(libs.zxing.core)
}
