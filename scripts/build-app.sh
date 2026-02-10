#!/bin/bash
set -e

APP_NAME="GoLocalServer"
BINARY_NAME="GoLocalServer"
VERSION="1.0.0"

# Get script directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

# Build directory
BUILD_DIR="$PROJECT_ROOT/build"
DIST_DIR="$PROJECT_ROOT/dist"
APP_BUNDLE="$BUILD_DIR/$APP_NAME.app"

echo "🔨 Building $APP_NAME v$VERSION..."

# Clean previous builds
rm -rf "$BUILD_DIR" "$DIST_DIR"
mkdir -p "$BUILD_DIR" "$DIST_DIR"

# Build the binary
echo "📦 Building binary..."
cd "$PROJECT_ROOT"
go build -ldflags="-s -w" -o "$BUILD_DIR/$BINARY_NAME" ./cmd/golocal

# Create app bundle structure
echo "📁 Creating app bundle..."
mkdir -p "$APP_BUNDLE/Contents/MacOS"
mkdir -p "$APP_BUNDLE/Contents/Resources"

# Copy binary
cp "$BUILD_DIR/$BINARY_NAME" "$APP_BUNDLE/Contents/MacOS/"

# Create Info.plist
cat > "$APP_BUNDLE/Contents/Info.plist" << EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleName</key>
    <string>$APP_NAME</string>
    <key>CFBundleDisplayName</key>
    <string>$APP_NAME</string>
    <key>CFBundleIdentifier</key>
    <string>com.k1dev.golocalserver</string>
    <key>CFBundleVersion</key>
    <string>$VERSION</string>
    <key>CFBundleShortVersionString</key>
    <string>$VERSION</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>CFBundleExecutable</key>
    <string>$BINARY_NAME</string>
    <key>CFBundleIconFile</key>
    <string>AppIcon</string>
    <key>LSMinimumSystemVersion</key>
    <string>10.14</string>
    <key>NSHighResolutionCapable</key>
    <true/>
</dict>
</plist>
EOF

# Convert logo.png to icns if it exists
if [ -f "$PROJECT_ROOT/logo.png" ]; then
    echo "🎨 Converting logo to app icon..."
    ICONSET_DIR="$BUILD_DIR/AppIcon.iconset"
    mkdir -p "$ICONSET_DIR"
    
    # Create different sizes
    sips -z 16 16 "$PROJECT_ROOT/logo.png" --out "$ICONSET_DIR/icon_16x16.png" 2>/dev/null || cp "$PROJECT_ROOT/logo.png" "$ICONSET_DIR/icon_16x16.png"
    sips -z 32 32 "$PROJECT_ROOT/logo.png" --out "$ICONSET_DIR/icon_32x32.png" 2>/dev/null || cp "$PROJECT_ROOT/logo.png" "$ICONSET_DIR/icon_32x32.png"
    sips -z 64 64 "$PROJECT_ROOT/logo.png" --out "$ICONSET_DIR/icon_16x16@2x.png" 2>/dev/null || cp "$PROJECT_ROOT/logo.png" "$ICONSET_DIR/icon_16x16@2x.png"
    sips -z 128 128 "$PROJECT_ROOT/logo.png" --out "$ICONSET_DIR/icon_128x128.png" 2>/dev/null || cp "$PROJECT_ROOT/logo.png" "$ICONSET_DIR/icon_128x128.png"
    sips -z 256 256 "$PROJECT_ROOT/logo.png" --out "$ICONSET_DIR/icon_256x256.png" 2>/dev/null || cp "$PROJECT_ROOT/logo.png" "$ICONSET_DIR/icon_256x256.png"
    sips -z 512 512 "$PROJECT_ROOT/logo.png" --out "$ICONSET_DIR/icon_512x512.png" 2>/dev/null || cp "$PROJECT_ROOT/logo.png" "$ICONSET_DIR/icon_512x512.png"
    
    iconutil -c icns "$ICONSET_DIR" -o "$APP_BUNDLE/Contents/Resources/AppIcon.icns" 2>/dev/null || echo "⚠️  Could not create icns, app will use default icon"
    rm -rf "$ICONSET_DIR"
fi

# Copy required files into app bundle Resources
if [ -d "$PROJECT_ROOT/docker-compose.yml" ] || [ -f "$PROJECT_ROOT/docker-compose.yml" ]; then
    cp "$PROJECT_ROOT/docker-compose.yml" "$APP_BUNDLE/Contents/Resources/"
fi

if [ -d "$PROJECT_ROOT/apache" ]; then
    cp -r "$PROJECT_ROOT/apache" "$APP_BUNDLE/Contents/Resources/"
fi

if [ -d "$PROJECT_ROOT/php" ]; then
    cp -r "$PROJECT_ROOT/php" "$APP_BUNDLE/Contents/Resources/"
fi

if [ -d "$PROJECT_ROOT/pkg" ]; then
    cp -r "$PROJECT_ROOT/pkg" "$APP_BUNDLE/Contents/Resources/"
fi

# Create zip for distribution
echo "📦 Creating distribution archive..."
cd "$BUILD_DIR"
zip -r "$DIST_DIR/$APP_NAME-v$VERSION-macOS.zip" "$APP_NAME.app"

# Also copy app bundle to dist
cp -r "$APP_BUNDLE" "$DIST_DIR/"

echo ""
echo "✅ Build complete!"
echo ""
echo "📍 Location: $DIST_DIR/$APP_NAME.app"
echo "📦 Archive: $DIST_DIR/$APP_NAME-v$VERSION-macOS.zip"
echo ""
echo "🚀 To run:"
echo "   1. Unzip the archive"
echo "   2. Double-click $APP_NAME.app"
echo "   3. If macOS warns about unidentified developer:"
echo "      - Right-click the app → Open"
echo "      - Or: System Preferences → Security & Privacy → Open Anyway"
echo ""
