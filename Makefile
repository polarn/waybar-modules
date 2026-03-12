.PHONY: build clean

build:
	go build -o ./build/waybar-gitlab-mr ./cmd/waybar-gitlab-mr
	go build -o ./build/waybar-wiim-nowplaying ./cmd/waybar-wiim-nowplaying

clean:
	rm -rf ./build
