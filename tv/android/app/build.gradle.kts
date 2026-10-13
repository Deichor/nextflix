plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.compose)
}

// Release key from scripts/build.sh (tv/android/.signing, gitignored); without it the release build is unsigned
val keystore: String? = System.getenv("NEXTFLIX_KEYSTORE")

android {
    namespace = "com.nextflix.tv"
    compileSdk = libs.versions.compileSdk.get().toInt()

    defaultConfig {
        applicationId = "com.nextflix.tv"
        minSdk = libs.versions.minSdk.get().toInt()
        targetSdk = libs.versions.targetSdk.get().toInt()
        versionCode = providers.gradleProperty("nextflix.versionCode").get().toInt()
        versionName = providers.gradleProperty("nextflix.versionName").get()
    }

    signingConfigs {
        if (keystore != null) {
            create("release") {
                storeFile = file(keystore)
                storePassword = System.getenv("NEXTFLIX_STORE_PASSWORD")
                keyAlias = System.getenv("NEXTFLIX_KEY_ALIAS") ?: "nextflix"
                keyPassword = System.getenv("NEXTFLIX_STORE_PASSWORD")
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = true
            isShrinkResources = true
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
            if (keystore != null) signingConfig = signingConfigs.getByName("release")
        }
    }
    buildFeatures {
        compose = true
        buildConfig = true
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}

dependencies {
    implementation(project(":core"))
    implementation(project(":feature:player"))
    implementation(project(":feature:subtitles"))
    implementation(project(":feature:browse"))
    implementation(project(":feature:detail"))
    implementation(project(":feature:search"))
    implementation(project(":feature:account"))
    implementation(libs.androidx.tvprovider)
    // Installs the baseline profile (src/main/baseline-prof.txt) so the first launch runs precompiled code
    implementation(libs.androidx.profileinstaller)

    debugImplementation(libs.compose.ui.tooling)
}
