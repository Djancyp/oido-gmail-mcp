.PHONY: build clean dist

PLUGIN_NAME := oido-gmail
BINARY := $(PLUGIN_NAME)-mcp
DIST_DIR := dist

# Build MCP server
build:
	@echo "Building $(PLUGIN_NAME) MCP server..."
	CGO_ENABLED=0 go build -o $(BINARY) .
	@echo "✓ Built: $(BINARY)"
	@ls -lh $(BINARY)

# Package as zip for upload via plugin UI
dist: build
	@mkdir -p $(DIST_DIR)
	@echo "Packaging $(PLUGIN_NAME).zip..."
	@cd $(DIST_DIR) && zip -j ../$(DIST_DIR)/$(PLUGIN_NAME).zip \
		../oido-extension.json \
		../OIDO.md \
		../$(BINARY)
	@mkdir -p $(DIST_DIR)/commands
	@cp -r commands/* $(DIST_DIR)/commands/ 2>/dev/null || true
	@mkdir -p $(DIST_DIR)/skills/oido-gmail
	@cp -r skills/oido-gmail/* $(DIST_DIR)/skills/oido-gmail/ 2>/dev/null || true
	@cd $(DIST_DIR) && zip -r ../$(DIST_DIR)/$(PLUGIN_NAME).zip commands/ skills/ 2>/dev/null || true
	@echo "✓ Packaged: $(DIST_DIR)/$(PLUGIN_NAME).zip"
	@ls -lh $(DIST_DIR)/$(PLUGIN_NAME).zip
	@echo ""
	@echo "Next steps:"
	@echo "  1. Upload $(DIST_DIR)/$(PLUGIN_NAME).zip via Plugins UI"
	@echo "  2. After upload, auto-links to Qwen CLI"

clean:
	rm -f $(BINARY)
	rm -rf $(DIST_DIR)
	rm -f $(PLUGIN_NAME).zip
