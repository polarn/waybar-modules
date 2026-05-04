DESTDIR ?= $(HOME)/.local/bin

SWIFTBAR_BINS := waybar-github-pr waybar-wiim-nowplaying

.PHONY: build clean install build-swiftbar install-swiftbar

build:
	go build -o ./build/waybar-gitlab-mr ./cmd/waybar-gitlab-mr
	go build -o ./build/waybar-github-pr ./cmd/waybar-github-pr
	go build -o ./build/waybar-wiim-nowplaying ./cmd/waybar-wiim-nowplaying
	go build -o ./build/waybar-cpu-temp ./cmd/waybar-cpu-temp
	go build -o ./build/waybar-gpu-temp ./cmd/waybar-gpu-temp
	go build -o ./build/waybar-tradfri-auth ./cmd/waybar-tradfri-auth
	go build -o ./build/waybar-tradfri ./cmd/waybar-tradfri
	go build -o ./build/tradfri-ctl ./cmd/tradfri-ctl
	go build -o ./build/waybar-allsvenskan ./cmd/waybar-allsvenskan
	go build -o ./build/waybar-batteries ./cmd/waybar-batteries

build-swiftbar:
	@mkdir -p ./build
	@for name in $(SWIFTBAR_BINS); do \
		go build -o ./build/$$name ./cmd/$$name || exit 1; \
	done

install: build
	@for bin in ./build/*; do \
		name=$$(basename $$bin); \
		rm -f $(DESTDIR)/$$name; \
		cp $$bin $(DESTDIR)/; \
		pkill -x $$name 2>/dev/null || true; \
	done

# pkill ends the streamable subprocess; SwiftBar re-execs the plugin script
# (which exec's the freshly-installed binary) automatically.
install-swiftbar: build-swiftbar
	@mkdir -p $(DESTDIR)
	@for name in $(SWIFTBAR_BINS); do \
		rm -f $(DESTDIR)/$$name; \
		cp ./build/$$name $(DESTDIR)/; \
		pkill -x $$name 2>/dev/null || true; \
	done

clean:
	rm -rf ./build
