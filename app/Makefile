# The Android app: the node in Go, bound with gomobile into an AAR, and the
# app around it in Kotlin. See README.md.

ANDROID_HOME ?= $(HOME)/Library/Android/sdk
ANDROID_NDK_HOME ?= $(lastword $(sort $(wildcard $(ANDROID_HOME)/ndk/*)))
export ANDROID_HOME ANDROID_NDK_HOME

AAR := android/app/libs/makocore.aar
APK := android/app/build/outputs/apk/debug/app-debug.apk

.PHONY: android core proto install clean

android: core
	cd android && ./gradlew :app:assembleDebug

# core builds the node. anet, which libp2p lists Android's network
# interfaces with, reaches into the standard library by linkname.
core:
	mkdir -p $(dir $(AAR))
	cd mobile && GOWORK=off gomobile bind \
		-target=android/arm64,android/amd64 -androidapi 26 \
		-javapkg dev.ironpark.mako.core \
		-ldflags "-checklinkname=0 -s -w" \
		-o ../$(AAR) .

# proto generates the protocol's messages for the app alone; the root's
# `make proto` does too. The generated Java is committed, as the Go is.
proto:
	cd .. && buf generate --template app/android/buf.gen.yaml

install: android
	adb install -r $(APK)

clean:
	rm -rf $(dir $(AAR)) android/app/build android/build
