.PHONY: build test run install install-headless clean icon verify

APP_NAME := NDev Pressure
EXECUTABLE := NDevPressure
TRACE_HELPER := NDevPressureTraceHelper
TRACE_HELPER_LABEL := com.nstranquist.ndev-pressure.trace-helper
CODE_SIGN_IDENTITY ?= -
CODE_SIGN_FLAGS := --force --sign "$(CODE_SIGN_IDENTITY)"
# The service-management entitlement is restricted: AMFI refuses to spawn an
# ad-hoc-signed binary that carries it (launchd error 153). Ad-hoc builds sign
# without entitlements — the trace helper stays fail-closed unavailable.
ENTITLEMENTS_FLAGS :=
ifneq ($(CODE_SIGN_IDENTITY),-)
CODE_SIGN_FLAGS += --options runtime --timestamp
ENTITLEMENTS_FLAGS := --entitlements Resources/NDevPressure.entitlements
endif
BUILD_DIR := .build/release
APP_DIR := $(BUILD_DIR)/$(APP_NAME).app
INSTALLED_APP := /Applications/$(APP_NAME).app
ICONSET := .build/AppIcon.iconset
ICON := .build/AppIcon.icns

build: icon
	swift build -c release
	@mkdir -p "$(APP_DIR)/Contents/MacOS" "$(APP_DIR)/Contents/Resources" "$(APP_DIR)/Contents/Library/LaunchServices" "$(APP_DIR)/Contents/Library/LaunchDaemons"
	@cp "$(BUILD_DIR)/$(EXECUTABLE)" "$(APP_DIR)/Contents/MacOS/$(EXECUTABLE)"
	@cp "$(BUILD_DIR)/$(TRACE_HELPER)" "$(APP_DIR)/Contents/Library/LaunchServices/$(TRACE_HELPER)"
	@cp "Resources/$(TRACE_HELPER_LABEL).plist" "$(APP_DIR)/Contents/Library/LaunchDaemons/$(TRACE_HELPER_LABEL).plist"
	@cp Resources/Info.plist "$(APP_DIR)/Contents/Info.plist"
	@if [ -f "$(ICON)" ]; then cp "$(ICON)" "$(APP_DIR)/Contents/Resources/AppIcon.icns"; fi
	@plutil -lint "$(APP_DIR)/Contents/Info.plist" "$(APP_DIR)/Contents/Library/LaunchDaemons/$(TRACE_HELPER_LABEL).plist" >/dev/null
	@codesign $(CODE_SIGN_FLAGS) --identifier "$(TRACE_HELPER_LABEL)" "$(APP_DIR)/Contents/Library/LaunchServices/$(TRACE_HELPER)" >/dev/null
	@codesign $(CODE_SIGN_FLAGS) --deep $(ENTITLEMENTS_FLAGS) "$(APP_DIR)" >/dev/null
	@echo "  Built: $(APP_DIR)"

icon: Resources/AppIcon.svg
	@mkdir -p .build
	@rm -rf "$(ICONSET)"
	@mkdir -p "$(ICONSET)"
	@sips -s format png Resources/AppIcon.svg --out .build/AppIcon-1024.png >/dev/null 2>&1 || \
	  rsvg-convert -w 1024 -h 1024 Resources/AppIcon.svg -o .build/AppIcon-1024.png 2>/dev/null || \
	  python3 script/svg_to_png.py Resources/AppIcon.svg .build/AppIcon-1024.png
	@sips -z 16 16 .build/AppIcon-1024.png --out "$(ICONSET)/icon_16x16.png" >/dev/null
	@sips -z 32 32 .build/AppIcon-1024.png --out "$(ICONSET)/icon_16x16@2x.png" >/dev/null
	@sips -z 32 32 .build/AppIcon-1024.png --out "$(ICONSET)/icon_32x32.png" >/dev/null
	@sips -z 64 64 .build/AppIcon-1024.png --out "$(ICONSET)/icon_32x32@2x.png" >/dev/null
	@sips -z 128 128 .build/AppIcon-1024.png --out "$(ICONSET)/icon_128x128.png" >/dev/null
	@sips -z 256 256 .build/AppIcon-1024.png --out "$(ICONSET)/icon_128x128@2x.png" >/dev/null
	@sips -z 256 256 .build/AppIcon-1024.png --out "$(ICONSET)/icon_256x256.png" >/dev/null
	@sips -z 512 512 .build/AppIcon-1024.png --out "$(ICONSET)/icon_256x256@2x.png" >/dev/null
	@sips -z 512 512 .build/AppIcon-1024.png --out "$(ICONSET)/icon_512x512.png" >/dev/null
	@cp .build/AppIcon-1024.png "$(ICONSET)/icon_512x512@2x.png"
	@iconutil -c icns "$(ICONSET)" -o "$(ICON)"

test:
	swift test

install: build
	@pkill -x "$(EXECUTABLE)" 2>/dev/null || true
	@sleep 0.3
	@rm -rf "$(INSTALLED_APP)"
	@cp -R "$(APP_DIR)" "$(INSTALLED_APP)"
	@/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister -f "$(INSTALLED_APP)" 2>/dev/null || true
	@echo "  Installed: $(INSTALLED_APP)"

install-headless: build
	@ditto "$(APP_DIR)" "$(INSTALLED_APP)"
	@codesign --verify --deep --strict "$(INSTALLED_APP)"
	@echo "  Updated in place without relaunch: $(INSTALLED_APP)"

run: install
	@open -na "$(INSTALLED_APP)"

verify: test build
	@codesign --verify --deep --strict "$(APP_DIR)"
	@if [ "$(CODE_SIGN_IDENTITY)" = "-" ] && codesign -d --entitlements - "$(APP_DIR)" 2>/dev/null | grep -q 'com\.apple\.developer\.'; then \
	  echo "  FAIL: ad-hoc build carries restricted entitlements (AMFI will refuse to spawn it)"; exit 1; \
	fi
	@echo "  verify ok"

clean:
	swift package clean
	@rm -rf .build
