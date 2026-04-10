DESTDIR ?= $(HOME)/.local/bin

.PHONY: build clean install

build:
	go build -o ./build/waybar-gitlab-mr ./cmd/waybar-gitlab-mr
	go build -o ./build/waybar-wiim-nowplaying ./cmd/waybar-wiim-nowplaying

install: build
	@for bin in ./build/*; do \
		name=$$(basename $$bin); \
		rm -f $(DESTDIR)/$$name; \
		cp $$bin $(DESTDIR)/; \
		pkill -x $$name 2>/dev/null || true; \
	done

clean:
	rm -rf ./build
