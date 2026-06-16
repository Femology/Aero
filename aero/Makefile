#!/usr/bin/env make -f
# Aero build automation

SHELL := /bin/bash
ROOT  := $(dir $(abspath $(lastword $(MAKEFILE_LIST))))
GO    := go
CARGO := cargo

SDK_DIR    := $(ROOT)sdk/go
SHELL_DIR  := $(ROOT)shell
SCRIPTS    := $(ROOT)scripts
DEMO_DIR   := $(ROOT)examples/demo
OUT        := $(ROOT)dist
GOOS       ?= $(shell $(GO) env GOOS)
GOARCH     ?= $(shell $(GO) env GOARCH)

.PHONY: all clean sdk shell demo pack test

all: shell sdk

sdk:
	@echo ">> building Go SDK"
	cd $(SDK_DIR) && $(GO) build ./...

shell:
	@echo ">> building Rust shell (release)"
	cd $(SHELL_DIR) && $(CARGO) build --release
	@mkdir -p $(OUT)
	cp $(SHELL_DIR)/target/release/aero-shell $(OUT)/aero-shell$(if $(filter windows,$(GOOS)),.exe,)

demo: all
	@echo ">> building demo app"
	cd $(DEMO_DIR) && $(GO) build -o $(OUT)/aero-demo .
	@echo ">> run with: AERO_SHELL=$(OUT)/aero-shell $(OUT)/aero-demo"

pack:
	@echo ">> running packager (cross-compile + installer)"
	cd $(SCRIPTS) && $(GO) run packager.go \
		--root $(ROOT) \
		--app $(DEMO_DIR) \
		--out $(OUT) \
		--name AeroDemo \
		--bin aero-demo \
		--os $(GOOS) \
		--arch $(GOARCH)

test:
	cd $(SDK_DIR) && $(GO) test ./...
	cd $(SHELL_DIR) && $(CARGO) test

clean:
	rm -rf $(OUT)
	cd $(SHELL_DIR) && $(CARGO) clean
