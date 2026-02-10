#!/bin/bash

APP_NAME="GoLocalServer"
BUNDLE_ID="com.golocalserver.app"
VERSION="1.0.0"

echo "Building ${APP_NAME} v${VERSION}..."

cd "$(dirname "$0")/.."

echo "Downloading dependencies..."
go mod download

echo "Building binary..."
go build -ldflags "-s -w -X main.Version=${VERSION}" -o "bin/${APP_NAME}" ./cmd/golocal

echo "Building macOS app bundle..."
mkdir -p "bin/${APP_NAME}.app/Contents/MacOS"
mkdir -p "bin/${APP_NAME}.app/Contents/Resources"

cp "bin/${APP_NAME}" "bin/${APP_NAME}.app/Contents/MacOS/"

cat > "bin/${APP_NAME}.app/Contents/Info.plist" << EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleExecutable</key>
    <string>${APP_NAME}</string>
    <key>CFBundleIdentifier</key>
    <string>${BUNDLE_ID}</string>
    <key>CFBundleName</key>
    <string>${APP_NAME}</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>CFBundleShortVersionString</key>
    <string>${VERSION}</string>
    <key>LSMinimumSystemVersion</key>
    <string>10.15</string>
    <key>LSUIElement</key>
    <true/>
</dict>
</plist>
EOF

echo "Build complete!"
echo "Binary: bin/${APP_NAME}"
echo "App Bundle: bin/${APP_NAME}.app"
