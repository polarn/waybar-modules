DESTDIR ?= $(HOME)/.local/bin

.PHONY: build clean install

build:
	go build -o ./build/waybar-gitlab-mr ./cmd/waybar-gitlab-mr
	go build -o ./build/waybar-wiim-nowplaying ./cmd/waybar-wiim-nowplaying

install: build
	@for bin in ./build/*; do \
		rm -f $(DESTDIR)/$$(basename $$bin); \
		cp $$bin $(DESTDIR)/; \
	done

clean:
	rm -rf ./build
