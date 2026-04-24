DESTDIR ?= $(HOME)/.local/bin

.PHONY: build clean install

build:
	go build -o ./build/waybar-gitlab-mr ./cmd/waybar-gitlab-mr
	go build -o ./build/waybar-github-pr ./cmd/waybar-github-pr
	go build -o ./build/waybar-wiim-nowplaying ./cmd/waybar-wiim-nowplaying
	go build -o ./build/waybar-cpu-temp ./cmd/waybar-cpu-temp
	go build -o ./build/waybar-gpu-temp ./cmd/waybar-gpu-temp
	go build -o ./build/waybar-tradfri-auth ./cmd/waybar-tradfri-auth
	go build -o ./build/waybar-tradfri ./cmd/waybar-tradfri
	go build -o ./build/tradfri-ctl ./cmd/tradfri-ctl

install: build
	@for bin in ./build/*; do \
		name=$$(basename $$bin); \
		rm -f $(DESTDIR)/$$name; \
		cp $$bin $(DESTDIR)/; \
		pkill -x $$name 2>/dev/null || true; \
	done

clean:
	rm -rf ./build
