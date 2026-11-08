plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.android)
    alias(libs.plugins.kotlin.serialization)
}

android {
    namespace = "dev.motty.app"
    compileSdk = 35

    defaultConfig {
        applicationId = "dev.motty.app"
        minSdk = 26
        targetSdk = 35
        versionCode = 1
        versionName = "0.3.6"
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
        ndk {
            // M1 ships arm64 only. Add x86_64 here when running an emulator.
            abiFilters += listOf("arm64-v8a")
        }
    }

    buildFeatures {
        compose = true
        buildConfig = true
    }
    composeOptions {
        kotlinCompilerExtensionVersion = libs.versions.composeCompiler.get()
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
    packaging {
        jniLibs {
            useLegacyPackaging = false
        }
    }
    buildTypes {
        release {
            isMinifyEnabled = false
            proguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "proguard-rules.pro",
            )
        }
    }
}

// The Go core is built by scripts/build-go.sh, never by Gradle: the script is
// the source of truth and keeps working on machines without a configured NDK.
val nativeLibrary = layout.projectDirectory.file("src/main/jniLibs/arm64-v8a/libmotty.so")
val checkGoCore = tasks.register("checkGoCore") {
    val library = nativeLibrary.asFile
    doFirst {
        if (!library.exists()) {
            throw GradleException(
                "libmotty.so is missing. Run android/scripts/build-go.sh before building the app.",
            )
        }
    }
}
tasks.named("preBuild") {
    dependsOn(checkGoCore)
}

dependencies {
    implementation(libs.androidx.core.ktx)
    implementation(libs.androidx.appcompat)
    implementation(libs.androidx.activity.compose)
    implementation(libs.androidx.lifecycle.runtime)
    implementation(libs.androidx.lifecycle.viewmodel.compose)
    implementation(libs.androidx.navigation.compose)
    implementation(platform(libs.compose.bom))
    implementation(libs.compose.ui)
    implementation(libs.compose.ui.graphics)
    implementation(libs.compose.ui.tooling.preview)
    implementation(libs.compose.material3)
    implementation(libs.compose.material.icons)
    implementation(libs.kotlinx.serialization.json)
    implementation(libs.kotlinx.coroutines.android)

    debugImplementation(libs.compose.ui.tooling)

    testImplementation(libs.junit)
    androidTestImplementation(libs.androidx.junit)
    androidTestImplementation(libs.androidx.test.core)
    androidTestImplementation(libs.espresso.core)
    androidTestImplementation(platform(libs.compose.bom))
}
