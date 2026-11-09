.PHONY: build test run install install-headless clean icon verify verify-signing

APP_NAME := NDev Pressure
EXECUTABLE := NDevPressure
TRACE_HELPER := NDevPressureTraceHelper
TRACE_HELPER_LABEL := com.nstranquist.ndev-pressure.trace-helper
# Prefer an installed Developer ID certificate. Ad hoc builds remain available
# for development, but cannot pass verification or replace the installed app.
DEVELOPER_ID := $(shell security find-identity -v -p codesigning 2>/dev/null | sed -n '/"Developer ID Application:/s/.* \([A-F0-9]\{40\}\) .*/\1/p' | head -n 1)
CODE_SIGN_IDENTITY ?= $(if $(DEVELOPER_ID),$(DEVELOPER_ID),-)
RELEASE_TOOL ?= $(HOME)/.local/bin/session-pressure-release
CODE_SIGN_FLAGS := --force --sign "$(CODE_SIGN_IDENTITY)"
# SMAppService registration for this non-sandboxed app needs no restricted
# entitlement. AMFI rejects unprovisioned restricted entitlements even with a
# Developer ID signature. Helper trust still requires the same real Team ID.
ifneq ($(CODE_SIGN_IDENTITY),-)
CODE_SIGN_FLAGS += --options runtime --timestamp
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
	@codesign $(CODE_SIGN_FLAGS) --deep "$(APP_DIR)" >/dev/null
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

install:
	@expected="$$("$(RELEASE_TOOL)" app-status --app "$(INSTALLED_APP)" --digest-only)" && \
	  $(MAKE) build && \
	  $(MAKE) verify-signing && \
	  "$(RELEASE_TOOL)" install-app --app "$(APP_DIR)" --destination "$(INSTALLED_APP)" --expected-current "$$expected"
	@pkill -x "$(EXECUTABLE)" 2>/dev/null || true
	@/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister -f "$(INSTALLED_APP)" 2>/dev/null || true

install-headless:
	@expected="$$("$(RELEASE_TOOL)" app-status --app "$(INSTALLED_APP)" --digest-only)" && \
	  $(MAKE) build && \
	  $(MAKE) verify-signing && \
	  "$(RELEASE_TOOL)" install-app --app "$(APP_DIR)" --destination "$(INSTALLED_APP)" --expected-current "$$expected"
	@echo "  Updated without relaunch: $(INSTALLED_APP)"

run: install
	@open -na "$(INSTALLED_APP)"

verify-signing:
	@codesign --verify --deep --strict -R='anchor apple generic' "$(APP_DIR)"
	@codesign --verify --strict -R='anchor apple generic' "$(APP_DIR)/Contents/Library/LaunchServices/$(TRACE_HELPER)"
	@app_team="$$(codesign -dv --verbose=4 "$(APP_DIR)" 2>&1 | sed -n 's/^TeamIdentifier=//p')"; \
	  helper_team="$$(codesign -dv --verbose=4 "$(APP_DIR)/Contents/Library/LaunchServices/$(TRACE_HELPER)" 2>&1 | sed -n 's/^TeamIdentifier=//p')"; \
	  if [ -z "$$app_team" ] || [ "$$app_team" = not\ set ] || [ "$$app_team" != "$$helper_team" ]; then \
	    echo "  FAIL: app and trace helper require the same real Team ID. Set CODE_SIGN_IDENTITY to an installed signing certificate."; exit 1; \
	  fi; \
	  echo "  Signing verified: Team ID $$app_team"

verify: test build
	@$(MAKE) verify-signing
	@if codesign -d --entitlements - "$(APP_DIR)" 2>/dev/null | grep -q 'com\.apple\.developer\.'; then \
	  echo "  FAIL: restricted entitlements require a provisioning profile; this app does not use them"; exit 1; \
	fi
	@echo "  verify ok"

clean:
	swift package clean
	@rm -rf .build
