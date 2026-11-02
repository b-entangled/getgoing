plugins {
    id("com.android.application")
}

val repositoryRoot = rootProject.projectDir.parentFile
val buildGuide by tasks.registering(Exec::class) {
    group = "build"
    description = "Render the repository learning guide for the offline Android app."
    workingDir(repositoryRoot)
    commandLine("go", "run", "./tools/buildsite", "-out", "site/index.html")
    inputs.files(fileTree(repositoryRoot) {
        include("**/*.md")
        exclude("android/**", ".git/**")
    })
    outputs.file(repositoryRoot.resolve("site/index.html"))
}
val guideAssets = layout.buildDirectory.dir("generated/guideAssets")
val prepareGuideAssets by tasks.registering(Copy::class) {
    dependsOn(buildGuide)
    from(repositoryRoot.resolve("site/index.html"))
    into(guideAssets)
}

android {
    namespace = "com.bentangled.learnings"
    compileSdk = 35

    defaultConfig {
        applicationId = "com.bentangled.learnings"
        minSdk = 24
        targetSdk = 34
        versionCode = 1
        versionName = "1.0"
    }

    sourceSets.getByName("main").assets.srcDir(guideAssets)

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}

tasks.named("preBuild").configure {
    dependsOn(prepareGuideAssets)
}
