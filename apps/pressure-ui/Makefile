.PHONY: test core-test build app run

APP_NAME := Session Pressure
APP_DIR := dist/$(APP_NAME).app
BIN := session-pressure-ui

core-test:
	cargo test -p pressure-core

test: core-test

build:
	cargo build -p session-pressure-ui

app: build
	rm -rf "$(APP_DIR)"
	mkdir -p "$(APP_DIR)/Contents/MacOS" "$(APP_DIR)/Contents/Resources"
	cp target/debug/$(BIN) "$(APP_DIR)/Contents/MacOS/$(BIN)"
	cp Resources/Info.plist "$(APP_DIR)/Contents/Info.plist"
	chmod +x "$(APP_DIR)/Contents/MacOS/$(BIN)"
	@echo "  Built $(APP_DIR)"

run: app
	open "$(APP_DIR)"
