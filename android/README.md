# Learnings Field Guide for Android

An offline-friendly Android reader for the learning materials in this
repository. It packages the interactive field guide in a native Android
WebView, so search, document navigation, section links, code examples,
theme switching and reading progress work without a network connection.
The vLLM guide is part of the LLM section.

The app does not request network access. Links to external references open
in the device browser; external fonts are optional and the guide falls
back to system fonts when offline.

## Build

Requirements: JDK 17, Android SDK Platform 35 and Build Tools, Gradle 8.9
or newer compatible with Android Gradle Plugin 8.7.3, and Go installed.
From the repository root:

```bash
gradle -p android assembleDebug
```

The Android pre-build task runs `go run ./tools/buildsite` and packages
the generated `site/index.html` as an app asset. To regenerate the browser
guide separately, run `make site`.

The debug APK is written to
`android/app/build/outputs/apk/debug/app-debug.apk`. Install it on a
connected device with:

```bash
adb install -r android/app/build/outputs/apk/debug/app-debug.apk
```
